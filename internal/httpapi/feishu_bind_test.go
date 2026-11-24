package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// feishuBindStartAs starts a login-only binding and returns the authorize
// query and the state.
func feishuBindStartAs(t *testing.T, h *authHandlers, session *http.Cookie) (url.Values, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/api/users/me/feishu/bind", nil)
	req.AddCookie(session)
	c.Request = req
	h.users.feishuBindStart(c)
	if rec.Code != http.StatusFound {
		t.Fatalf("bind start = %d %s", rec.Code, rec.Body.String())
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	return loc.Query(), loc.Query().Get("state")
}

func feishuUnbindAs(t *testing.T, h *authHandlers, session *http.Cookie) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodDelete, "/api/users/me/feishu/bind", nil)
	req.AddCookie(session)
	c.Request = req
	h.users.feishuUnbind(c)
	return rec.Code, rec.Body.String()
}

func feishuStatusOf(t *testing.T, h *authHandlers, session *http.Cookie) feishuGrantStatus {
	t.Helper()
	_, body := feishuMeRequest(t, h, http.MethodGet, session)
	var status feishuGrantStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("status %s: %v", body, err)
	}
	return status
}

func TestFeishuConnectAndBindLoginAtOnce(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	admin, _ := s.CreateLocalAccount("admin", "pw-secret-1", true)
	session := sessionCookie(t, client, admin)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/api/users/me/feishu/connect?preset=readonly&login=1", nil)
	req.AddCookie(session)
	c.Request = req
	h.users.feishuConnectStart(c)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	state := loc.Query().Get("state")

	cb := feishuConnectCallbackAs(t, h, session, state, "good")
	if cb.Code != http.StatusFound || cb.Header().Get("Location") != "/?feishu=connected-bound#/settings" {
		t.Fatalf("callback = %d %s %s", cb.Code, cb.Header().Get("Location"), cb.Body.String())
	}
	if g, ok := s.FeishuGrant(admin.ID); !ok || g.Status != repo.FeishuGrantActive {
		t.Fatalf("grant = %+v %v", g, ok)
	}
	if owner, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); !ok || owner.ID != admin.ID {
		t.Fatalf("login identity = %+v %v", owner, ok)
	}
	// The password still works, so the account may unbind.
	st := feishuStatusOf(t, h, session)
	if !st.LoginBound || !st.CanUnbindLogin || st.LoginName != "张三" {
		t.Fatalf("status = %+v", st)
	}
	if code, body := feishuUnbindAs(t, h, session); code != http.StatusOK || strings.Contains(body, `"loginBound":true`) {
		t.Fatalf("unbind = %d %s", code, body)
	}
	if _, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); ok {
		t.Fatal("unbind must remove the sign-in identity")
	}
	if g, _ := s.FeishuGrant(admin.ID); g.Status != repo.FeishuGrantActive {
		t.Fatal("unbinding login must keep the agents' grant")
	}
	if !s.CheckLocalPassword(admin.ID, "pw-secret-1") {
		t.Fatal("password changed")
	}
}

func TestFeishuBindOnlyForConnectedAccount(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	admin, _ := s.CreateLocalAccount("admin", "pw-secret-1", true)
	session := sessionCookie(t, client, admin)
	_, state, _ := feishuConnectStartAs(t, h, session, "readonly")
	if rec := feishuConnectCallbackAs(t, h, session, state, "good"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?feishu=connected#/settings" {
		t.Fatalf("connect = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if _, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); ok {
		t.Fatal("connect without 「同时用于登录」 must not bind")
	}
	query, state := feishuBindStartAs(t, h, session)
	if query.Get("scope") != "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("bind authorize query = %v (login only, with PKCE)", query)
	}
	before := len(fake.tokenForms)
	// A login-only authorization carries no offline_access, so no refresh token.
	rec := feishuConnectCallbackAs(t, h, session, state, "noref-bind")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?feishu=bound#/settings" {
		t.Fatalf("bind = %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if len(fake.tokenForms) != before+1 || fake.tokenForms[before].Get("code_verifier") == "" {
		t.Fatal("bind must exchange its code with the PKCE verifier")
	}
	if owner, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); !ok || owner.ID != admin.ID {
		t.Fatalf("login identity = %+v %v", owner, ok)
	}
	// Binding alone leaves the grant's tokens untouched.
	if got, err := h.feishuUserAccess(t.Context(), feishuConfigFromEnv(), admin.ID); err != nil || got.Token != "user-good" {
		t.Fatalf("grant token after bind = %q %v", got.Token, err)
	}

	// A login binding must match the connected identity.
	fake.identity.UnionID = "union-9"
	if err := s.UnbindIdentity(admin.ID, repo.IdentityProviderFeishu); err != nil {
		t.Fatal(err)
	}
	_, state = feishuBindStartAs(t, h, session)
	if rec := feishuConnectCallbackAs(t, h, session, state, "other"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "请用已连接的飞书账号绑定登录") {
		t.Fatalf("bind of another identity = %d %s", rec.Code, rec.Body.String())
	}
}

func TestFeishuBindRefusesIdentityOfAnotherAccount(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	// The member first signed in with Feishu, which auto-provisioned an account.
	provisioned, err := s.CreateIdentityAccount(repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三"}, false)
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := s.CreateLocalAccount("admin", "pw-secret-1", true)
	session := sessionCookie(t, client, admin)
	_, state := feishuBindStartAs(t, h, session)
	rec := feishuConnectCallbackAs(t, h, session, state, "good")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), provisioned.LoginName) || !strings.Contains(rec.Body.String(), "不会自动合并") {
		t.Fatalf("taken identity = %d %s", rec.Code, rec.Body.String())
	}
	if owner, _ := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); owner.ID != provisioned.ID {
		t.Fatalf("identity moved to %d", owner.ID)
	}
	if feishuStatusOf(t, h, session).LoginBound {
		t.Fatal("refused bind must not bind")
	}
	// Connecting with 「同时用于登录」 is refused before anything is stored.
	rec2 := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec2)
	req := httptest.NewRequest(http.MethodGet, "/api/users/me/feishu/connect?preset=readonly&login=1", nil)
	req.AddCookie(session)
	c.Request = req
	h.users.feishuConnectStart(c)
	loc, _ := url.Parse(rec2.Header().Get("Location"))
	if rec := feishuConnectCallbackAs(t, h, session, loc.Query().Get("state"), "good"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "不会自动合并") {
		t.Fatalf("connect+bind of a taken identity = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.FeishuGrant(admin.ID); ok {
		t.Fatal("a refused connect+bind must not store a grant")
	}
}

func TestFeishuBindTenantAllowlistAndMember(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	admin, _ := s.CreateLocalAccount("admin", "pw-secret-1", true)
	bob, _ := s.CreateLocalAccount("bob", "pw-secret-2", false)
	_, state := feishuBindStartAs(t, h, sessionCookie(t, client, admin))
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, bob), state, "good"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-member bind = %d", rec.Code)
	}
	fake.identity.TenantKey = "tenantX"
	_, state = feishuBindStartAs(t, h, sessionCookie(t, client, admin))
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, admin), state, "good"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenantX") {
		t.Fatalf("denied tenant bind = %d %s", rec.Code, rec.Body.String())
	}
	for _, id := range []int64{admin.ID, bob.ID} {
		if len(s.IdentitiesOf(id, repo.IdentityProviderFeishu)) != 0 {
			t.Fatalf("user %d was bound by a refused callback", id)
		}
	}
}

func TestFeishuUnbindKeepsOnlySignIn(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	member, err := s.CreateIdentityAccount(repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三"}, false)
	if err != nil {
		t.Fatal(err)
	}
	session := sessionCookie(t, client, member)
	if st := feishuStatusOf(t, h, session); !st.LoginBound || st.CanUnbindLogin {
		t.Fatalf("Feishu-only account status = %+v", st)
	}
	code, body := feishuUnbindAs(t, h, session)
	if code != http.StatusConflict || !strings.Contains(body, "唯一的登录方式") {
		t.Fatalf("unbind only sign-in = %d %s", code, body)
	}
	if _, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); !ok {
		t.Fatal("refused unbind removed the identity")
	}
}
