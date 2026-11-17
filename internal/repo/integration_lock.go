package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// localIntegrationLocks backs LockIntegration on sqlite, which only ever
// serves one process.
var localIntegrationLocks sync.Map

// LockIntegration serializes merges into a repository's integration branch
// across every control-plane instance. On MySQL it holds a named lock on a
// dedicated connection, so the lock also ends if the holder dies. The
// returned function releases it.
func (r *DeliveryRepo) LockIntegration(ctx context.Context, repositoryID string, wait time.Duration) (func(), error) {
	name := integrationLockName(repositoryID)
	if r.isPostgres() {
		return r.lockIntegrationPostgres(ctx, repositoryID, name, wait)
	}
	if !r.isMySQL() {
		m, _ := localIntegrationLocks.LoadOrStore(name, &sync.Mutex{})
		mu := m.(*sync.Mutex)
		mu.Lock()
		return mu.Unlock, nil
	}
	db, err := r.DB.DB()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, int(wait/time.Second)).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		return nil, fmt.Errorf("等待仓库 %s 的合入锁超时（%s）", repositoryID, wait)
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", name)
		_ = conn.Close()
	}, nil
}

// lockIntegrationPostgres is the PostgreSQL counterpart of GET_LOCK: a
// session-level advisory lock keyed by hashtext(name) on a dedicated
// connection, so the lock also ends if the holder dies. lock_timeout bounds
// the wait like GET_LOCK's timeout argument.
// ponytail: hashtext is 32-bit; two repository names can collide and then
// merely serialize each other, which is safe.
func (r *DeliveryRepo) lockIntegrationPostgres(ctx context.Context, repositoryID, name string, wait time.Duration) (func(), error) {
	db, err := r.DB.DB()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	ms := max(wait.Milliseconds(), 1)
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET lock_timeout = %d", ms)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", name)
	_, _ = conn.ExecContext(context.Background(), "RESET lock_timeout")
	if err != nil {
		_ = conn.Close()
		if strings.Contains(err.Error(), "55P03") {
			return nil, fmt.Errorf("等待仓库 %s 的合入锁超时（%s）", repositoryID, wait)
		}
		return nil, err
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", name)
		_ = conn.Close()
	}, nil
}

// integrationLockName fits MySQL's 64-character lock name limit.
func integrationLockName(repositoryID string) string {
	id := strings.TrimSpace(repositoryID)
	if id == "" {
		id = "_"
	}
	name := "agentflow:integrate:" + id
	if len(name) <= 64 {
		return name
	}
	return "agentflow:integrate:" + hexSHA256(id)[:40]
}
