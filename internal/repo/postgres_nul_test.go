package repo

import (
	"testing"

	"gorm.io/gorm"
)

// mustPostgresDialector is a PostgreSQL dialector that never connects, for
// tests that only inspect generated SQL or types.
func mustPostgresDialector(t *testing.T) gorm.Dialector {
	t.Helper()
	d, err := newPostgresDialector("postgres://u:p@127.0.0.1:1/x")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEscapeNUL(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		json     bool
	}{
		{"plain", "plain", false},
		{"a\x00b\x00", `a\u0000b\u0000`, false},
		{`{"a":"x\u0000y"}`, `{"a":"x\\u0000y"}`, true},
		// An escaped backslash followed by u0000 is literal text, not NUL.
		{`{"a":"\\u0000"}`, `{"a":"\\u0000"}`, true},
		{`{"a":"\\\u0000"}`, `{"a":"\\\\u0000"}`, true},
		{`{"a":"\u00001"}`, `{"a":"\\u00001"}`, true},
		{`{"a":"\n"}`, `{"a":"\n"}`, true},
	} {
		if got := string(escapeNUL([]byte(tc.in), tc.json)); got != tc.want {
			t.Errorf("escapeNUL(%q, json=%v) = %q, want %q", tc.in, tc.json, got, tc.want)
		}
	}
}
