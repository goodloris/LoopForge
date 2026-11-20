package runtime

import (
	"strings"
	"testing"
)

// An empty AGENTFLOW_KIRO_DATA_DIR warns; only the compose mount gets the .env hint.
func TestKiroDataDirWarning(t *testing.T) {
	t.Setenv(kiroDataDirEnv, "")
	if got := KiroDataDirWarning(); got != "" {
		t.Fatalf("unset dir warned: %q", got)
	}
	dir := t.TempDir()
	t.Setenv(kiroDataDirEnv, dir)
	got := KiroDataDirWarning()
	if !strings.Contains(got, dir+" contains no data.sqlite3") || strings.Contains(got, "LOOPFORGE_KIRO_DATA_DIR") {
		t.Fatalf("custom dir warning = %q", got)
	}
	t.Setenv(kiroDataDirEnv, "/kiro-host")
	if got := KiroDataDirWarning(); !strings.Contains(got, "LOOPFORGE_KIRO_DATA_DIR") || !strings.Contains(got, "docker compose up -d") {
		t.Fatalf("compose mount warning = %q", got)
	}
}
