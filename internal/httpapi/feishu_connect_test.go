package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func sessionCookie(t *testing.T, client *auth.Client, user domain.User) *http.Cookie {
	t.Helper()
	u := domainUserToAuth(user)
	u.AuthTime = time.Now().UTC()
	token, err := client.SignUser(u)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: authCookieName, Value: token}
}

// feishuConnectStartAs starts a connection as user and returns the
// authorize URL and the state cookie.
func feishuConnectStartAs(t *testing.T, h *authHandlers, session *http.Cookie, preset string) (*url.URL, string, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/api/users/me/feishu/connect?preset="+url.QueryEscape(preset), nil)
	if session != nil {
		req.AddCookie(session)
	}
	c.Request = req
	h.users.feishuConnectStart(c)
	if rec.Code != http.StatusFound {
		return nil, "", rec
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == feishuStateCookieName {
			return loc, ck.Value, rec
		}
	}
	t.Fatal("connect start set no state cookie")
	return nil, "", rec
}

func feishuConnectCallbackAs(t *testing.T, h *authHandlers, session *http.Cookie, state, code string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/feishu/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	req.AddCookie(&http.Cookie{Name: feishuStateCookieName, Value: state})
	if session != nil {
		req.AddCookie(session)
	}
	c.Request = req
	h.feishuCallback(c)
	return rec
}

func feishuMeRequest(t *testing.T, h *authHandlers, method string, session *http.Cookie) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, "/api/users/me/feishu", nil)
	if session != nil {
		req.AddCookie(session)
	}
	c.Request = req
	if method == http.MethodDelete {
		h.users.feishuDisconnect(c)
	} else {
		h.users.feishuStatus(c)
	}
	return rec.Code, rec.Body.String()
}

func TestFeishuConnectPasswordAccount(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	alice, err := s.CreateLocalAccount("alice", "pw-secret-1", false)
	if err != nil {
		t.Fatal(err)
	}
	session := sessionCookie(t, client, alice)

	loc, state, _ := feishuConnectStartAs(t, h, session, "")
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != fake.server.URL+"/open-apis/authen/v1/authorize" {
		t.Fatalf("authorize endpoint = %s", got)
	}
	q := loc.Query()
	scopes := strings.Fields(q.Get("scope"))
	if len(scopes) == 0 || scopes[0] != "offline_access" || !strings.Contains(q.Get("scope"), "docx:document:readonly") || strings.Contains(q.Get("scope"), "im:message.send_as_user") {
		t.Fatalf("default (readonly) scope = %q", q.Get("scope"))
	}
	if q.Get("state") != state || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("authorize query = %v", q)
	}

	rec := feishuConnectCallbackAs(t, h, session, state, "good")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?feishu=connected#/settings" {
		t.Fatalf("callback = %d %s body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	// PKCE: the verifier sent to the token endpoint hashes to the challenge.
	form := fake.tokenForms[len(fake.tokenForms)-1]
	if form.Get("grant_type") != "authorization_code" || pkceChallenge(form.Get("code_verifier")) != q.Get("code_challenge") {
		t.Fatalf("token form = %v (challenge %s)", form, q.Get("code_challenge"))
	}
	grant, ok := s.FeishuGrant(alice.ID)
	if !ok || grant.Status != repo.FeishuGrantActive || grant.Name != "张三" || grant.Tenant != "tenantA" || grant.Subject != "union-1" || grant.Preset != "readonly" {
		t.Fatalf("grant = %+v %v", grant, ok)
	}
	// A connection is not a sign-in binding.
	if _, bound := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); bound {
		t.Fatal("connecting must not create a Feishu sign-in identity")
	}
	if ids := s.IdentitiesOf(alice.ID, repo.IdentityProviderFeishu); len(ids) != 0 {
		t.Fatalf("password account gained identities %v", ids)
	}

	code, body := feishuMeRequest(t, h, http.MethodGet, session)
	var status feishuGrantStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil || code != http.StatusOK {
		t.Fatalf("status = %d %s", code, body)
	}
	if !status.Enabled || !status.Connected || status.Name != "张三" || status.Preset != "readonly" || len(status.Presets) != 2 {
		t.Fatalf("status = %+v", status)
	}
	if strings.Contains(body, "user-good") || strings.Contains(body, "refresh-good") {
		t.Fatalf("status leaks a token: %s", body)
	}
}

func TestFeishuConnectPresets(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	session := sessionCookie(t, client, alice)
	loc, _, _ := feishuConnectStartAs(t, h, session, "readwrite")
	if scope := loc.Query().Get("scope"); !strings.Contains(scope, "im:message.send_as_user") || !strings.Contains(scope, "docx:document:readonly") {
		t.Fatalf("readwrite scope = %q", scope)
	}
	if _, _, rec := feishuConnectStartAs(t, h, session, "admin"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown preset = %d", rec.Code)
	}
	if _, _, rec := feishuConnectStartAs(t, h, nil, "readonly"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out start = %d", rec.Code)
	}
}

func TestFeishuConnectStateIsBoundToMember(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	bob, _ := s.CreateLocalAccount("bob", "pw-secret-2", false)
	_, state, _ := feishuConnectStartAs(t, h, sessionCookie(t, client, alice), "readonly")
	// Bob's browser cannot finish Alice's authorization.
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, bob), state, "good"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-member callback = %d", rec.Code)
	}
	_, state2, _ := feishuConnectStartAs(t, h, sessionCookie(t, client, alice), "readonly")
	if rec := feishuConnectCallbackAs(t, h, nil, state2, "good"); rec.Code != http.StatusForbidden {
		t.Fatalf("signed-out callback = %d", rec.Code)
	}
	for _, id := range []int64{alice.ID, bob.ID} {
		if _, ok := s.FeishuGrant(id); ok {
			t.Fatalf("user %d got a grant from a rejected callback", id)
		}
	}
	// The state is single use: replaying it is refused before any exchange.
	_, state3, _ := feishuConnectStartAs(t, h, sessionCookie(t, client, alice), "readonly")
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, alice), state3, "good"); rec.Code != http.StatusFound {
		t.Fatalf("first use = %d %s", rec.Code, rec.Body.String())
	}
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, alice), state3, "good"); rec.Code != http.StatusBadRequest {
		t.Fatalf("replay = %d", rec.Code)
	}
	// An expired or invalid code maps to a retry page.
	_, state4, _ := feishuConnectStartAs(t, h, sessionCookie(t, client, alice), "readonly")
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, alice), state4, "expired"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "授权码无效或已过期") {
		t.Fatalf("expired code = %d %s", rec.Code, rec.Body.String())
	}
}

func TestFeishuConnectIdentityConsistency(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	// A Feishu sign-in account may only connect its own identity.
	member, err := s.CreateIdentityAccount(repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-2", Name: "李四"}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ := feishuConnectStartAs(t, h, sessionCookie(t, client, member), "readonly")
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, member), state, "good"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "请用登录本平台的飞书账号授权") {
		t.Fatalf("other identity = %d %s", rec.Code, rec.Body.String())
	}
	fake.identity.UnionID = "union-2"
	_, state, _ = feishuConnectStartAs(t, h, sessionCookie(t, client, member), "readonly")
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, member), state, "good"); rec.Code != http.StatusFound {
		t.Fatalf("own identity = %d %s", rec.Code, rec.Body.String())
	}
	// A password account cannot connect an identity another account signs in with.
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	_, state, _ = feishuConnectStartAs(t, h, sessionCookie(t, client, alice), "readonly")
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, alice), state, "good"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "已被其他账号使用") {
		t.Fatalf("taken identity = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.FeishuGrant(alice.ID); ok {
		t.Fatal("a refused connection must not store a grant")
	}
}

func TestFeishuConnectTenantWhitelist(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.identity.TenantKey = "tenantX"
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	_, state, _ := feishuConnectStartAs(t, h, sessionCookie(t, client, alice), "readonly")
	if rec := feishuConnectCallbackAs(t, h, sessionCookie(t, client, alice), state, "good"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenantX") {
		t.Fatalf("denied tenant = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.FeishuGrant(alice.ID); ok {
		t.Fatal("a denied tenant must not store a grant")
	}
}

func TestFeishuDisconnectRevokes(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	session := sessionCookie(t, client, alice)
	_, state, _ := feishuConnectStartAs(t, h, session, "readonly")
	if rec := feishuConnectCallbackAs(t, h, session, state, "good"); rec.Code != http.StatusFound {
		t.Fatalf("connect = %d", rec.Code)
	}
	code, body := feishuMeRequest(t, h, http.MethodDelete, session)
	if code != http.StatusOK || !strings.Contains(body, `"status":"revoked"`) || strings.Contains(body, `"connected":true`) {
		t.Fatalf("disconnect = %d %s", code, body)
	}
	if len(fake.revoked) != 1 || fake.revoked[0] != "refresh-good" {
		t.Fatalf("revoked at Feishu = %v", fake.revoked)
	}
	if _, err := h.feishuUserAccess(context.Background(), feishuConfigFromEnv(), alice.ID); !errors.Is(err, repo.ErrFeishuNotConnected) {
		t.Fatalf("token after disconnect err = %v", err)
	}
	if code, _ := feishuMeRequest(t, h, http.MethodGet, nil); code != http.StatusUnauthorized {
		t.Fatalf("signed-out status = %d", code)
	}
}

func TestFeishuUserAccessRefreshesThroughFeishu(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, _ := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	if err := s.SaveFeishuGrant(alice.ID, repo.FeishuGrantInput{Tenant: "tenantA", Subject: "union-1", Name: "张三", Preset: "readonly", Tokens: repo.FeishuTokens{
		AccessToken: "user-old", AccessExpiresAt: time.Now().Add(10 * time.Minute), RefreshToken: "refresh-old", RefreshExpiresAt: time.Now().Add(time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := feishuConfigFromEnv()
	got, err := h.feishuUserAccess(context.Background(), cfg, alice.ID)
	if err != nil || got.Token != "user-refreshed-refresh-old" {
		t.Fatalf("refreshed = %+v err=%v", got, err)
	}
	if form := fake.tokenForms[len(fake.tokenForms)-1]; form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "refresh-old" {
		t.Fatalf("refresh form = %v", form)
	}
	// A refresh token Feishu revoked expires the grant: no user identity.
	if err := s.SaveFeishuGrant(alice.ID, repo.FeishuGrantInput{Tenant: "tenantA", Subject: "union-1", Tokens: repo.FeishuTokens{
		AccessToken: "user-old", AccessExpiresAt: time.Now().Add(time.Minute), RefreshToken: "refresh-old", RefreshExpiresAt: time.Now().Add(time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	fake.refreshCode = 20064
	if _, err := h.feishuUserAccess(context.Background(), cfg, alice.ID); !errors.Is(err, repo.ErrFeishuNotConnected) {
		t.Fatalf("revoked refresh err = %v", err)
	}
	if g, _ := s.FeishuGrant(alice.ID); g.Status != repo.FeishuGrantExpired {
		t.Fatalf("status = %q want expired", g.Status)
	}
	// A server error is transient: the grant stays active.
	if err := s.SaveFeishuGrant(alice.ID, repo.FeishuGrantInput{Tenant: "tenantA", Subject: "union-1", Tokens: repo.FeishuTokens{
		AccessToken: "user-old", AccessExpiresAt: time.Now().Add(time.Minute), RefreshToken: "refresh-old", RefreshExpiresAt: time.Now().Add(time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	fake.refreshCode = 20050
	if _, err := h.feishuUserAccess(context.Background(), cfg, alice.ID); err == nil || errors.Is(err, repo.ErrFeishuNotConnected) {
		t.Fatalf("transient err = %v", err)
	}
	if g, _ := s.FeishuGrant(alice.ID); g.Status != repo.FeishuGrantActive {
		t.Fatalf("status after 20050 = %q", g.Status)
	}
}

func TestFeishuTenantTokenCached(t *testing.T) {
	fake := newFakeFeishu(t)
	newFeishuTestHandler(t, fake.server.URL)
	client := newFeishuClient(feishuConfigFromEnv())
	for i := 0; i < 3; i++ {
		tok, err := client.tenantAccessToken(context.Background())
		if err != nil || tok != "t-xyz" {
			t.Fatalf("tenant token = %q %v", tok, err)
		}
	}
	if fake.tenantCalls != 1 {
		t.Fatalf("tenant token fetches = %d, want 1 (cached)", fake.tenantCalls)
	}
}

func TestFeishuStatusUnconfigured(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	t.Setenv("LOOPFORGE_FEISHU_APP_ID", "")
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	code, body := feishuMeRequest(t, h, http.MethodGet, sessionCookie(t, client, alice))
	if code != http.StatusOK || body != `{"enabled":false,"connected":false}` {
		t.Fatalf("unconfigured status = %d %s", code, body)
	}
}
