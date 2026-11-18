package repo

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// Tests that need real row locks, isolation levels or a database clock run
// against a disposable database server: the MySQL database named by the first
// set variable in mysqlEnv (AGENTFLOW_TEST_MYSQL_DSN when none is given), or
// else a fresh schema on the AGENTFLOW_TEST_PG_DSN server. sqlite reproduces
// none of this, so they are skipped there.

// serverTestPeers opens another store on the database behind a server store.
var serverTestPeers sync.Map // *Store -> func() (*Store, error)

func serverTestStore(t *testing.T, mysqlEnv ...string) *Store {
	t.Helper()
	if len(mysqlEnv) == 0 {
		mysqlEnv = []string{"AGENTFLOW_TEST_MYSQL_DSN"}
	}
	for _, name := range mysqlEnv {
		if dsn := os.Getenv(name); dsn != "" {
			open := func() (*Store, error) { return Open(dsn) }
			s, err := open()
			if err != nil {
				// Connection errors can contain credential material.
				t.Fatalf("open MySQL test store (%T); DSN omitted", err)
			}
			t.Cleanup(s.Close)
			serverTestPeers.Store(s, open)
			return s
		}
	}
	if os.Getenv("AGENTFLOW_TEST_PG_DSN") == "" {
		t.Skip("a disposable MySQL or PostgreSQL server is required")
	}
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	serverTestPeers.Store(s, func() (*Store, error) { return OpenTestPeer(s) })
	return s
}

// serverTestPeer is another API replica: a store with its own connection pool
// on the same database as s.
func serverTestPeer(t *testing.T, s *Store) *Store {
	t.Helper()
	open, ok := serverTestPeers.Load(s)
	if !ok {
		t.Fatal("serverTestPeer needs a store from serverTestStore")
	}
	peer, err := open.(func() (*Store, error))()
	if err != nil {
		t.Fatalf("open peer test store (%T)", err)
	}
	t.Cleanup(peer.Close)
	return peer
}

// skewDBClock pins s to one connection and returns a function that moves the
// database clock of that connection to real time plus d.
// MySQL uses SET timestamp. PostgreSQL has no equivalent, so the store's
// schema gets its own statement_timestamp() that adds a session setting, and
// is searched before pg_catalog; production SQL calls the unqualified name.
func skewDBClock(t *testing.T, s *Store) func(time.Duration) {
	t.Helper()
	sqlDB, err := s.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	// The session settings below live on that one connection.
	sqlDB.SetConnMaxIdleTime(0)
	if s.isMySQL() {
		t.Cleanup(func() { s.DB.Exec("SET timestamp = DEFAULT") })
		return func(d time.Duration) {
			t.Helper()
			if err := s.DB.Exec("SET timestamp = ?", float64(time.Now().Add(d).UnixMicro())/1e6).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if !s.isPostgres() {
		t.Fatal("skewDBClock needs a MySQL or PostgreSQL store")
	}
	var schema string
	if err := s.DB.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`CREATE OR REPLACE FUNCTION statement_timestamp() RETURNS timestamptz LANGUAGE sql STABLE AS
		$$ SELECT pg_catalog.statement_timestamp() + COALESCE(NULLIF(current_setting('lf.clock_skew', true), ''), '0')::interval $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("SET search_path = " + schema + ", pg_catalog").Error; err != nil {
		t.Fatal(err)
	}
	return func(d time.Duration) {
		t.Helper()
		if err := s.DB.Exec(fmt.Sprintf("SET lf.clock_skew = '%d microseconds'", d.Microseconds())).Error; err != nil {
			t.Fatal(err)
		}
		// The shadow function must be what production SQL resolves.
		var skewed time.Time
		if err := s.DB.Raw("SELECT " + s.dbClockSQL()).Scan(&skewed).Error; err != nil {
			t.Fatal(err)
		}
		if off := skewed.Sub(time.Now()) - d; off < -10*time.Second || off > 10*time.Second {
			t.Fatalf("database clock is %v off the requested skew %v", off, d)
		}
	}
}
