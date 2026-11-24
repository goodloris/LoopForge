package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Kiro CLI has no key, yet it can be saved as the personal default executor
// wherever this account can run it (the platform's or a machine's kiro-cli
// login), and is refused as a default where it cannot.
func TestKiroPersonalDefaultExecutorFollowsTheLogin(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	f := cvBNewHTTPFixture(t)
	path := fmt.Sprintf("/api/users/%d/credentials", f.user.ID)
	body := map[string]any{"assistantDefaults": map[string]any{"executor": "kiro", "model": "auto"}}

	kiroCLIInstalled = func() bool { return false }
	if rec := cvBRequest(t, f, http.MethodPut, path, body, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "已配置的执行器") {
		t.Fatalf("without a login = %d %s", rec.Code, rec.Body.String())
	}

	kiroCLIInstalled = func() bool { return true }
	if rec := cvBRequest(t, f, http.MethodPut, path, body, true); rec.Code != http.StatusOK {
		t.Fatalf("with a login = %d %s", rec.Code, rec.Body.String())
	}
	user, _ := f.s.User(f.user.ID)
	if user.AssistantDefaults.Executor != "kiro" || user.AssistantDefaults.Model != "auto" {
		t.Fatalf("saved defaults = %+v", user.AssistantDefaults)
	}
	// The catalog reports it as the preference, and feature models follow it.
	if got := personalDefaultModel(user); got != "kiro:auto" {
		t.Fatalf("personal default model = %q", got)
	}
}
