package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// openEmptyUserStore is a test store without any user row. OpenTest seeds the
// demo-owner user (no local password account), so tests of the first-account
// rule start from a deployment nobody has signed in to yet.
func openEmptyUserStore(t *testing.T) *repo.Store {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.DB.Exec("DELETE FROM users").Error; err != nil {
		t.Fatal(err)
	}
	if n := s.UserCount(); n != 0 {
		t.Fatalf("empty store still has %d users", n)
	}
	return s
}

// signupServer drives the full HTTP API (New + ServeHTTP) with Feishu sign-in
// configured against a fake Feishu.
type signupServer struct {
	t    *testing.T
	s    *repo.Store
	h    http.Handler
	fake *fakeFeishu
}

var signupTestIPs atomic.Int32

func newSignupServer(t *testing.T, s *repo.Store, feishu bool) *signupServer {
	t.Helper()
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	fake := newFakeFeishu(t)
	if feishu {
		t.Setenv("LOOPFORGE_FEISHU_APP_ID", "app-id")
		t.Setenv("LOOPFORGE_FEISHU_APP_SECRET", "app-secret")
	} else {
		t.Setenv("LOOPFORGE_FEISHU_APP_ID", "")
		t.Setenv("LOOPFORGE_FEISHU_APP_SECRET", "")
	}
	t.Setenv("LOOPFORGE_FEISHU_BASE_URL", fake.server.URL)
	t.Setenv("LOOPFORGE_FEISHU_ALLOWED_TENANTS", "tenantA")
	if s == nil {
		var err error
		if s, err = repo.OpenTest(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
	}
	return &signupServer{t: t, s: s, h: New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", ""), fake: fake}
}

// call sends one request. ip "" picks a fresh client address so the per-IP
// password budget never leaks between steps.
func (v *signupServer) call(method, path, ip, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		r.Header.Set("Origin", "http://localhost")
	}
	if ip == "" {
		n := signupTestIPs.Add(1)
		ip = fmt.Sprintf("203.0.%d.%d", n/250%250, n%250+1)
	}
	r.RemoteAddr = ip + ":1234"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	v.h.ServeHTTP(w, r)
	return w
}

func (v *signupServer) register(login string) *httptest.ResponseRecorder {
	return v.call("POST", "/api/auth/register", "", `{"login":"`+login+`","password":"a-long-enough-password"}`)
}

func (v *signupServer) signIn(login, password string) *httptest.ResponseRecorder {
	return v.call("POST", "/api/auth/password", "", `{"login":"`+login+`","password":"`+password+`"}`)
}

func authCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == authCookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

func (v *signupServer) setup() map[string]any {
	v.t.Helper()
	w := v.call("GET", "/api/auth/setup", "", "")
	var out map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		v.t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	return out
}

// feishuSignIn runs start + callback through the router for the identity the
// fake Feishu currently returns.
func (v *signupServer) feishuSignIn(code string) *httptest.ResponseRecorder {
	v.t.Helper()
	start := v.call("GET", "/api/auth/feishu/start", "", "")
	if start.Code != http.StatusFound {
		v.t.Fatalf("feishu start: %d %s", start.Code, start.Body.String())
	}
	loc, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		v.t.Fatal(err)
	}
	state := loc.Query().Get("state")
	var stateCookie *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == feishuStateCookieName {
			stateCookie = c
		}
	}
	if state == "" || stateCookie == nil {
		v.t.Fatalf("feishu start without state: %s", start.Header().Get("Location"))
	}
	return v.call("GET", "/api/auth/feishu/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), "", "", stateCookie)
}

// The four combinations of the requirement table (plus both unset).
func TestPasswordSignupMatrix(t *testing.T) {
	cases := []struct {
		name, allow, allowPassword string
		signup, passwordSignup     bool
	}{
		{"defaults", "", "", true, true},
		{"signup-true-password-unset", "true", "", true, true},
		{"signup-true-password-false", "true", "false", true, false},
		{"signup-false-password-unset", "false", "", false, false},
		{"signup-false-password-true", "false", "true", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENTFLOW_ALLOW_SIGNUP", tc.allow)
			t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", tc.allowPassword)
			v := newSignupServer(t, nil, true)
			s := v.s

			// demo-owner is seeded, so this is not a fresh deployment.
			setup := v.setup()
			if setup["needsSetup"] != false || setup["signupEnabled"] != tc.signup || setup["passwordSignupEnabled"] != tc.passwordSignup || setup["feishuEnabled"] != true {
				t.Fatalf("setup = %v", setup)
			}

			if _, err := s.CreateLocalAccount("owner", "owner-password", true); err != nil {
				t.Fatal(err)
			}
			owner := authCookie(v.signIn("owner", "owner-password"))
			if owner == nil {
				t.Fatal("owner sign-in failed")
			}
			w := v.call("GET", "/api/platform/accounts", "", "", owner)
			var listed struct{ SignupEnabled, PasswordSignupEnabled bool }
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || listed.SignupEnabled != tc.signup || listed.PasswordSignupEnabled != tc.passwordSignup {
				t.Fatalf("accounts: %d %s", w.Code, w.Body.String())
			}

			reg := v.register("visitor")
			if tc.passwordSignup {
				if reg.Code != 200 || !strings.Contains(reg.Body.String(), `"admin":false`) {
					t.Fatalf("register: %d %s", reg.Code, reg.Body.String())
				}
			} else {
				want := signupClosedMessage
				if tc.signup {
					want = signupClosedFeishuMessage
				}
				if reg.Code != 403 || !strings.Contains(reg.Body.String(), want) {
					t.Fatalf("register: %d %s (want 403 %q)", reg.Code, reg.Body.String(), want)
				}
				if _, ok := s.UserByLoginName("visitor"); ok {
					t.Fatal("refused registration created an account")
				}
			}

			// A new Feishu user from an allowlisted tenant.
			before := s.UserCount()
			cb := v.feishuSignIn("good-new")
			if tc.signup {
				if cb.Code != http.StatusFound || authCookie(cb) == nil || s.UserCount() != before+1 {
					t.Fatalf("feishu new user: %d users %d->%d %s", cb.Code, before, s.UserCount(), cb.Body.String())
				}
			} else if cb.Code != http.StatusForbidden || s.UserCount() != before {
				t.Fatalf("feishu new user with signup off: %d users %d->%d", cb.Code, before, s.UserCount())
			}

			// An already-bound member signs in under every combination.
			v.fake.identity.UnionID = "union-bound"
			if _, err := s.CreateIdentityAccount(repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-bound", Name: "成员"}, false); err != nil {
				t.Fatal(err)
			}
			before = s.UserCount()
			if cb := v.feishuSignIn("good-bound"); cb.Code != http.StatusFound || authCookie(cb) == nil || s.UserCount() != before {
				t.Fatalf("bound member: %d %s", cb.Code, cb.Body.String())
			}
		})
	}
}

// ALLOW_SIGNUP=true + ALLOW_PASSWORD_SIGNUP=false closes only the sign-up form.
func TestPasswordSignupClosedKeepsOtherPaths(t *testing.T) {
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "true")
	t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", "false")
	v := newSignupServer(t, nil, true)
	s := v.s
	if _, err := s.CreateLocalAccount("owner", "owner-password", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLocalAccount("member", "member-password", false); err != nil {
		t.Fatal(err)
	}

	if w := v.register("visitor"); w.Code != 403 || !strings.Contains(w.Body.String(), signupClosedFeishuMessage) {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	if w := v.signIn("member", "member-password"); w.Code != 200 || authCookie(w) == nil {
		t.Fatalf("existing account sign-in: %d %s", w.Code, w.Body.String())
	}
	owner := authCookie(v.signIn("owner", "owner-password"))
	if w := v.call("POST", "/api/platform/accounts", "", `{"loginName":"alice","password":"alice-password"}`, owner); w.Code != http.StatusCreated {
		t.Fatalf("admin creates account: %d %s", w.Code, w.Body.String())
	}
	if w := v.signIn("alice", "alice-password"); w.Code != 200 {
		t.Fatalf("created account sign-in: %d", w.Code)
	}
	before := s.UserCount()
	if cb := v.feishuSignIn("good-new"); cb.Code != http.StatusFound || s.UserCount() != before+1 {
		t.Fatalf("feishu provisioning: %d %s", cb.Code, cb.Body.String())
	}
	if _, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); !ok {
		t.Fatal("feishu identity not bound")
	}

	// Without Feishu sign-in the refusal keeps the original wording.
	t.Setenv("LOOPFORGE_FEISHU_APP_ID", "")
	if w := v.register("visitor"); w.Code != 403 || !strings.Contains(w.Body.String(), signupClosedMessage) || strings.Contains(w.Body.String(), "飞书") {
		t.Fatalf("register without feishu: %d %s", w.Code, w.Body.String())
	}
}

// A closed sign-up is refused before it spends the per-IP password budget.
func TestPasswordSignupRejectsBeforeLimiter(t *testing.T) {
	for _, env := range []struct{ allow, allowPassword string }{{"true", "false"}, {"false", ""}} {
		t.Run(env.allow+"/"+env.allowPassword, func(t *testing.T) {
			t.Setenv("AGENTFLOW_ALLOW_SIGNUP", env.allow)
			t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", env.allowPassword)
			v := newSignupServer(t, nil, true)
			if _, err := v.s.CreateLocalAccount("owner", "owner-password", true); err != nil {
				t.Fatal(err)
			}
			const ip = "192.0.2.77"
			for i := 0; i < passwordAttemptLimit+3; i++ {
				w := v.call("POST", "/api/auth/register", ip, `{"login":"visitor","password":"a-long-enough-password"}`)
				if w.Code != 403 {
					t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
				}
			}
			if w := v.call("POST", "/api/auth/password", ip, `{"login":"owner","password":"owner-password"}`); w.Code != 200 {
				t.Fatalf("sign-in after refused sign-ups: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPasswordSignupEnvParsing(t *testing.T) {
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "")
	for _, off := range []string{"false", " OFF ", "0", "No"} {
		t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", off)
		if passwordSignupEnabled() || !signupEnabled() {
			t.Fatalf("%q: password=%v signup=%v", off, passwordSignupEnabled(), signupEnabled())
		}
	}
	for _, on := range []string{"", "true", "garbage"} {
		t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", on)
		if !passwordSignupEnabled() {
			t.Fatalf("%q must inherit AGENTFLOW_ALLOW_SIGNUP", on)
		}
	}
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
	t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", "true")
	if passwordSignupEnabled() || signupEnabled() {
		t.Fatal("AGENTFLOW_ALLOW_PASSWORD_SIGNUP cannot reopen sign-up")
	}
}

// The first account is decided by the total user count for both password
// registration and needsSetup.
func TestFirstAccountCountsAllUsers(t *testing.T) {
	t.Run("feishu-only deployment", func(t *testing.T) {
		t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "")
		t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", "")
		v := newSignupServer(t, openEmptyUserStore(t), true)
		if cb := v.feishuSignIn("good-first"); cb.Code != http.StatusFound {
			t.Fatalf("first feishu sign-in: %d %s", cb.Code, cb.Body.String())
		}
		if v.s.LocalAccountCount() != 0 || v.s.UserCount() != 1 {
			t.Fatalf("users=%d local=%d", v.s.UserCount(), v.s.LocalAccountCount())
		}
		if setup := v.setup(); setup["needsSetup"] != false {
			t.Fatalf("feishu-only deployment needs no setup: %v", setup)
		}
		w := v.register("visitor")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"admin":false`) || v.s.IsPlatformAdmin("visitor") {
			t.Fatalf("password register must not become admin: %d %s", w.Code, w.Body.String())
		}
		t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
		if w := v.register("other"); w.Code != 403 {
			t.Fatalf("closed signup on a feishu-only deployment: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("empty deployment", func(t *testing.T) {
		t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "")
		t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", "")
		v := newSignupServer(t, openEmptyUserStore(t), true)
		if setup := v.setup(); setup["needsSetup"] != true || setup["signupEnabled"] != true || setup["passwordSignupEnabled"] != true {
			t.Fatalf("empty deployment: %v", setup)
		}
		if w := v.register("owner"); w.Code != 200 || !strings.Contains(w.Body.String(), `"admin":true`) || !v.s.IsPlatformAdmin("owner") {
			t.Fatalf("first register: %d %s", w.Code, w.Body.String())
		}
		if setup := v.setup(); setup["needsSetup"] != false {
			t.Fatalf("after setup: %v", setup)
		}
	})
	for _, env := range []struct{ allow, allowPassword string }{{"true", "false"}, {"false", ""}} {
		t.Run("empty deployment closed "+env.allow+"/"+env.allowPassword, func(t *testing.T) {
			t.Setenv("AGENTFLOW_ALLOW_SIGNUP", env.allow)
			t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", env.allowPassword)
			v := newSignupServer(t, openEmptyUserStore(t), true)
			if setup := v.setup(); setup["needsSetup"] != true || setup["signupEnabled"] != true || setup["passwordSignupEnabled"] != true {
				t.Fatalf("empty deployment: %v", setup)
			}
			if w := v.register("owner"); w.Code != 200 || !strings.Contains(w.Body.String(), `"admin":true`) {
				t.Fatalf("first account is always allowed: %d %s", w.Code, w.Body.String())
			}
			if w := v.register("second"); w.Code != 403 {
				t.Fatalf("second account with closed sign-up: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// With both variables unset the responses keep their previous shape; setup only
// gains passwordSignupEnabled.
func TestPasswordSignupDefaultsUnchanged(t *testing.T) {
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "")
	t.Setenv("AGENTFLOW_ALLOW_PASSWORD_SIGNUP", "")
	v := newSignupServer(t, nil, false)
	setup := v.setup()
	if got := strings.Join(sortedKeys(setup), ","); got != "feishuEnabled,needsSetup,passwordSignupEnabled,signupEnabled" {
		t.Fatalf("setup keys = %s", got)
	}
	if setup["needsSetup"] != false || setup["signupEnabled"] != true || setup["passwordSignupEnabled"] != true || setup["feishuEnabled"] != false {
		t.Fatalf("setup = %v", setup)
	}
	if _, err := v.s.CreateLocalAccount("owner", "owner-password", true); err != nil {
		t.Fatal(err)
	}
	owner := authCookie(v.signIn("owner", "owner-password"))
	w := v.call("GET", "/api/platform/accounts", "", "", owner)
	var listed map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil {
		t.Fatalf("accounts: %d %s", w.Code, w.Body.String())
	}
	if got := strings.Join(sortedKeys(listed), ","); got != "accounts,feishuEnabled,passwordSignupEnabled,signupEnabled" || listed["signupEnabled"] != true || listed["passwordSignupEnabled"] != true {
		t.Fatalf("accounts = %v", listed)
	}

	// The refusal text under AGENTFLOW_ALLOW_SIGNUP=false is unchanged.
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
	if w := v.register("visitor"); w.Code != 403 || w.Body.String() != `{"error":"自助注册已关闭，请联系管理员创建账号"}` {
		t.Fatalf("closed register: %d %s", w.Code, w.Body.String())
	}
}
