package httpapi

import (
	"encoding/json"
	"io"
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

// fakeFeishu is a stand-in for open.feishu.cn covering the three sign-in calls.
// The identity it returns from user_info is controlled per test.
type fakeFeishu struct {
	server   *httptest.Server
	identity feishuUserInfo
	appCalls int
}

func newFakeFeishu(t *testing.T) *fakeFeishu {
	t.Helper()
	f := &fakeFeishu{identity: feishuUserInfo{OpenID: "open-1", UnionID: "union-1", Name: "张三", AvatarURL: "https://feishu.example/a.png", TenantKey: "tenantA"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/app_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		f.appCalls++
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["app_id"] == "" || body["app_secret"] == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "bad app"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "app_access_token": "app-xyz", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/authen/v1/oidc/access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer app-xyz" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991661, "msg": "bad app token"})
			return
		}
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		switch body["code"] {
		case "expired":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 20004, "msg": "code expired"})
		case "invalid":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 20003, "msg": "code invalid"})
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 20003, "msg": "no code"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"access_token": "user-" + body["code"]}})
		}
	})
	mux.HandleFunc("/open-apis/authen/v1/user_info", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer user-") {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991661, "msg": "bad user token"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"open_id":    f.identity.OpenID,
			"union_id":   f.identity.UnionID,
			"name":       f.identity.Name,
			"avatar_url": f.identity.AvatarURL,
			"tenant_key": f.identity.TenantKey,
		}})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func newFeishuTestHandler(t *testing.T, base string) (*authHandlers, *repo.Store, *auth.Client) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	t.Setenv("LOOPFORGE_FEISHU_APP_ID", "app-id")
	t.Setenv("LOOPFORGE_FEISHU_APP_SECRET", "app-secret")
	t.Setenv("LOOPFORGE_FEISHU_BASE_URL", base)
	t.Setenv("LOOPFORGE_FEISHU_ALLOWED_TENANTS", "tenantA, tenantC")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	client := auth.NewClient(auth.Config{SessionSecret: "feishu-test-secret"})
	return &authHandlers{deps: &deps{repo: s, authClient: client, feishuStates: newFeishuStateStore()}}, s, client
}

func feishuStartFlow(t *testing.T, h *authHandlers) (state, cookie string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/feishu/start", nil)
	h.feishuStart(c)
	if rec.Code != http.StatusFound {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), feishuAuthorizeURL) {
		t.Fatalf("authorize URL = %s", rec.Header().Get("Location"))
	}
	state = loc.Query().Get("state")
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == feishuStateCookieName {
			cookie = ck.Value
		}
	}
	if state == "" || cookie == "" || state != cookie {
		t.Fatalf("state=%q cookie=%q", state, cookie)
	}
	return state, cookie
}

func feishuCallback(t *testing.T, h *authHandlers, stateQuery, stateCookie, code string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	target := "/api/auth/feishu/callback?state=" + url.QueryEscape(stateQuery) + "&code=" + url.QueryEscape(code)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if stateCookie != "" {
		req.AddCookie(&http.Cookie{Name: feishuStateCookieName, Value: stateCookie})
	}
	c.Request = req
	h.feishuCallback(c)
	return rec
}

func signedCookieUser(t *testing.T, client *auth.Client, rec *httptest.ResponseRecorder) *auth.User {
	t.Helper()
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == authCookieName && ck.Value != "" {
			u, err := client.ParseSignedUser(ck.Value)
			if err != nil {
				t.Fatalf("parse auth cookie: %v", err)
			}
			return u
		}
	}
	t.Fatalf("no auth cookie set; body=%s", rec.Body.String())
	return nil
}

func TestFeishuLoginProvisionsAndRebinds(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	// A welcome project new accounts should join.
	if err := s.PutProject(domain.Project{ID: welcomeProjectID, Key: "W", Name: "Welcome"}); err != nil {
		t.Fatal(err)
	}
	before := s.UserCount()

	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good-1")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d body=%s", rec.Code, rec.Body.String())
	}
	user := signedCookieUser(t, client, rec)
	if user.IsGuest || !strings.HasPrefix(user.LoginName, "feishu-") {
		t.Fatalf("signed user = %+v", user)
	}
	if user.ChineseName != "张三" {
		t.Fatalf("chinese name = %q", user.ChineseName)
	}
	if user.AvatarURL != "https://feishu.example/a.png" {
		t.Fatalf("avatar = %q (should be the Feishu avatar)", user.AvatarURL)
	}
	if got := s.UserCount(); got != before+1 {
		t.Fatalf("user count = %d want %d", got, before+1)
	}
	bound, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1")
	if !ok || bound.ID != user.ID {
		t.Fatalf("identity not bound: %+v %v", bound, ok)
	}
	project, _ := s.Project(welcomeProjectID)
	joined := false
	for _, m := range project.Members {
		if m.UserID == bound.ID {
			joined = true
		}
	}
	if !joined {
		t.Fatal("new feishu user did not join the welcome project")
	}

	// A second sign-in (fresh state) hits the same user, not a new one.
	state2, cookie2 := feishuStartFlow(t, h)
	rec2 := feishuCallback(t, h, state2, cookie2, "good-2")
	if rec2.Code != http.StatusFound {
		t.Fatalf("second callback status = %d", rec2.Code)
	}
	user2 := signedCookieUser(t, client, rec2)
	if user2.ID != user.ID {
		t.Fatalf("second login created a new user: %d vs %d", user2.ID, user.ID)
	}
	if got := s.UserCount(); got != before+1 {
		t.Fatalf("second login changed user count to %d", got)
	}
}

func TestFeishuLoginStateChecks(t *testing.T) {
	fake := newFakeFeishu(t)
	h, _, _ := newFeishuTestHandler(t, fake.server.URL)

	// Missing state and cookie.
	if rec := feishuCallback(t, h, "", "", "good"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing state = %d", rec.Code)
	}
	// State in the query but no cookie.
	state, cookie := feishuStartFlow(t, h)
	if rec := feishuCallback(t, h, state, "", "good"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing cookie = %d", rec.Code)
	}
	// Cookie and query disagree.
	state2, _ := feishuStartFlow(t, h)
	if rec := feishuCallback(t, h, state2, "different-cookie", "good"); rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched state = %d", rec.Code)
	}
	// An unknown state (never issued) is rejected by the server-side store.
	if rec := feishuCallback(t, h, "never-issued", "never-issued", "good"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown state = %d", rec.Code)
	}
	// Replay: the first (state2, cookie) pair above is still unused; use it once
	// and then again — the single-use store must reject the replay.
	ok := feishuCallback(t, h, state, cookie, "good") // 'state' was issued and still unused
	if ok.Code != http.StatusFound {
		t.Fatalf("first use = %d body=%s", ok.Code, ok.Body.String())
	}
	replay := feishuCallback(t, h, state, cookie, "good")
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed state must be rejected, got %d", replay.Code)
	}
}

func TestFeishuLoginStateExpires(t *testing.T) {
	fake := newFakeFeishu(t)
	h, _, _ := newFeishuTestHandler(t, fake.server.URL)
	now := time.Now()
	h.feishuStates = &feishuStateStore{entries: map[string]feishuStateEntry{}, now: func() time.Time { return now }}

	state, cookie := feishuStartFlow(t, h)
	now = now.Add(feishuStateTTL + time.Minute)
	rec := feishuCallback(t, h, state, cookie, "good")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired state = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFeishuLoginTenantWhitelist(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.identity.TenantKey = "tenantX" // not in the whitelist
	h, s, _ := newFeishuTestHandler(t, fake.server.URL)
	before := s.UserCount()
	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied tenant status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tenantX") {
		t.Fatalf("denial page should show the tenant_key: %s", rec.Body.String())
	}
	if s.UserCount() != before {
		t.Fatal("a denied tenant must not create an account")
	}
}

func TestFeishuLoginDoesNotTakeOverPasswordAccount(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.identity.Name = "admin" // same display name as a password account
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	if _, err := s.CreateLocalAccount("admin", "pw-secret", true); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.UserByLoginName("admin")

	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback = %d body=%s", rec.Code, rec.Body.String())
	}
	user := signedCookieUser(t, client, rec)
	if user.ID == admin.ID || user.LoginName == "admin" {
		t.Fatalf("feishu login took over the password account: %+v", user)
	}
	// The password account still authenticates with its password and gained no
	// Feishu binding.
	if !s.CheckLocalPassword(admin.ID, "pw-secret") {
		t.Fatal("password account changed")
	}
	if _, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); !ok {
		t.Fatal("feishu identity should bind to the new account")
	}
}

func TestFeishuLoginSignupDisabled(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, _ := newFeishuTestHandler(t, fake.server.URL)
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
	before := s.UserCount() // demo-owner already present, so a new member is not the first account
	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("signup-disabled status = %d body=%s", rec.Code, rec.Body.String())
	}
	if s.UserCount() != before {
		t.Fatal("signup disabled must not provision a new user")
	}

	// An already-bound member can still sign in when signup is disabled.
	bound, err := s.CreateIdentityAccount(repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三"}, false)
	if err != nil {
		t.Fatal(err)
	}
	state2, cookie2 := feishuStartFlow(t, h)
	rec2 := feishuCallback(t, h, state2, cookie2, "good")
	if rec2.Code != http.StatusFound {
		t.Fatalf("bound member with signup disabled = %d", rec2.Code)
	}
	if u := signedCookieUser(t, auth.NewClient(auth.Config{SessionSecret: "feishu-test-secret"}), rec2); u.ID != bound.ID {
		t.Fatalf("bound member signed in as %d want %d", u.ID, bound.ID)
	}
}

func TestFeishuLoginExpiredCode(t *testing.T) {
	fake := newFakeFeishu(t)
	h, _, _ := newFeishuTestHandler(t, fake.server.URL)
	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "expired")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired code status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "授权码无效或已过期") {
		t.Fatalf("expired code page = %s", rec.Body.String())
	}
}

func TestFeishuDisabledWhenUnconfigured(t *testing.T) {
	t.Setenv("LOOPFORGE_FEISHU_APP_ID", "")
	t.Setenv("LOOPFORGE_FEISHU_APP_SECRET", "")
	if feishuSignInEnabled() {
		t.Fatal("feishu should be disabled without app credentials")
	}
	t.Setenv("LOOPFORGE_FEISHU_APP_ID", "app-id")
	t.Setenv("LOOPFORGE_FEISHU_APP_SECRET", "app-secret")
	if !feishuSignInEnabled() {
		t.Fatal("feishu should be enabled with app credentials")
	}
}
