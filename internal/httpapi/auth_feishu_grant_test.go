package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/repo"
)

// feishuTestGrant stores an active grant for (tenant, subject) on userID, as
// connecting Feishu from 个人配置 → 飞书 does.
func feishuTestGrant(t *testing.T, s *repo.Store, userID int64, tenant, subject string) {
	t.Helper()
	if err := s.SaveFeishuGrant(userID, repo.FeishuGrantInput{
		Tenant: tenant, Subject: subject, OpenID: "open-1", Name: "张三", Preset: "readonly",
		Tokens: repo.FeishuTokens{AccessToken: "acc", RefreshToken: "ref"},
	}); err != nil {
		t.Fatal(err)
	}
}

func hasAuthCookie(rec *httptest.ResponseRecorder) bool {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == authCookieName && ck.Value != "" {
			return true
		}
	}
	return false
}

// assertFeishuExistingAccountPage checks the sign-in stopped at the guidance
// page: no account, no binding, no session.
func assertFeishuExistingAccountPage(t *testing.T, s *repo.Store, rec *httptest.ResponseRecorder, before int64, logins ...string) {
	t.Helper()
	body := rec.Body.String()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", rec.Code, body)
	}
	for _, want := range append([]string{"请先用该账号登录", "个人配置 → 飞书", "同时用于登录"}, logins...) {
		if !strings.Contains(body, want) {
			t.Fatalf("guidance page lacks %q: %s", want, body)
		}
	}
	if got := s.UserCount(); got != before {
		t.Fatalf("user count = %d want %d (no account may be provisioned)", got, before)
	}
	if hasAuthCookie(rec) {
		t.Fatal("guidance page must not sign anyone in")
	}
	if _, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); ok {
		t.Fatal("guidance page must not bind the identity")
	}
}

func TestFeishuLoginDetectsActiveGrant(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, _ := newFeishuTestHandler(t, fake.server.URL)
	alice, err := s.CreateLocalAccount("alice", "pw-secret-1", false)
	if err != nil {
		t.Fatal(err)
	}
	feishuTestGrant(t, s, alice.ID, "tenantA", "union-1")
	before := s.UserCount()

	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	assertFeishuExistingAccountPage(t, s, rec, before, "<code>alice</code>")
	if strings.Contains(rec.Body.String(), "union-1") {
		t.Fatalf("guidance page leaks the identity: %s", rec.Body.String())
	}
}

func TestFeishuLoginDetectsRevokedAndExpiredGrant(t *testing.T) {
	for _, status := range []string{repo.FeishuGrantRevoked, repo.FeishuGrantExpired} {
		t.Run(status, func(t *testing.T) {
			fake := newFakeFeishu(t)
			h, s, _ := newFeishuTestHandler(t, fake.server.URL)
			alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
			feishuTestGrant(t, s, alice.ID, "tenantA", "union-1")
			if status == repo.FeishuGrantRevoked {
				if _, err := s.RevokeFeishuGrant(alice.ID); err != nil {
					t.Fatal(err)
				}
			} else if err := s.DB.Exec("UPDATE user_feishu_grants SET status = ? WHERE user_id = ?", status, alice.ID).Error; err != nil {
				t.Fatal(err)
			}
			if g, _ := s.FeishuGrant(alice.ID); g.Status != status {
				t.Fatalf("grant status = %q", g.Status)
			}
			before := s.UserCount()
			state, cookie := feishuStartFlow(t, h)
			assertFeishuExistingAccountPage(t, s, feishuCallback(t, h, state, cookie, "good"), before, "alice")
		})
	}
}

func TestFeishuLoginBoundUserUnaffected(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	feishuTestGrant(t, s, alice.ID, "tenantA", "union-1")
	if err := s.BindIdentity(alice.ID, repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三"}); err != nil {
		t.Fatal(err)
	}
	before := s.UserCount()
	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	if rec.Code != http.StatusFound {
		t.Fatalf("bound sign-in = %d %s", rec.Code, rec.Body.String())
	}
	if u := signedCookieUser(t, client, rec); u.ID != alice.ID {
		t.Fatalf("signed in as %d want %d", u.ID, alice.ID)
	}
	if s.UserCount() != before {
		t.Fatal("bound sign-in must not provision")
	}
}

func TestFeishuLoginGrantHolderWithOwnFeishuLoginExcluded(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	feishuTestGrant(t, s, alice.ID, "tenantA", "union-1")
	// alice signs in with a different Feishu identity, so union-1 is not hers to claim.
	if err := s.BindIdentity(alice.ID, repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-2"}); err != nil {
		t.Fatal(err)
	}
	before := s.UserCount()
	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	if rec.Code != http.StatusFound {
		t.Fatalf("sign-in = %d %s", rec.Code, rec.Body.String())
	}
	if u := signedCookieUser(t, client, rec); u.ID == alice.ID || !strings.HasPrefix(u.LoginName, "feishu-") {
		t.Fatalf("signed in as %+v, want a provisioned account", u)
	}
	if s.UserCount() != before+1 {
		t.Fatal("expected the usual auto-provisioning")
	}
}

func TestFeishuLoginMultipleGrantHolders(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, _ := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	bob, _ := s.CreateLocalAccount("bob<x>", "pw-secret-2", false)
	feishuTestGrant(t, s, alice.ID, "tenantA", "union-1")
	feishuTestGrant(t, s, bob.ID, "tenantA", "union-1")
	before := s.UserCount()
	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	assertFeishuExistingAccountPage(t, s, rec, before, "<code>alice</code>、<code>bob&lt;x&gt;</code>")
}

func TestFeishuLoginGrantHolderSignupDisabled(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, _ := newFeishuTestHandler(t, fake.server.URL)
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	feishuTestGrant(t, s, alice.ID, "tenantA", "union-1")
	before := s.UserCount()
	state, cookie := feishuStartFlow(t, h)
	assertFeishuExistingAccountPage(t, s, feishuCallback(t, h, state, cookie, "good"), before, "alice")

	// Without a holder the signup switch still refuses as before.
	fake.identity.UnionID = "union-new"
	state, cookie = feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "自助注册已关闭") {
		t.Fatalf("signup disabled = %d %s", rec.Code, rec.Body.String())
	}
	if s.UserCount() != before {
		t.Fatal("signup disabled must not provision")
	}
}

func TestFeishuLoginGrantTenantWhitelistFirst(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.identity.TenantKey = "tenantX"
	h, s, _ := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	feishuTestGrant(t, s, alice.ID, "tenantX", "union-1")
	state, cookie := feishuStartFlow(t, h)
	rec := feishuCallback(t, h, state, cookie, "good")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenantX") || strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("denied tenant = %d %s", rec.Code, rec.Body.String())
	}
}

// Acceptance 2: after following the guidance, Feishu sign-in enters account A.
func TestFeishuLoginAfterGuidedBind(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	alice, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	session := sessionCookie(t, client, alice)
	// Connect without 「同时用于登录」.
	_, state, _ := feishuConnectStartAs(t, h, session, "readonly")
	if rec := feishuConnectCallbackAs(t, h, session, state, "good"); rec.Code != http.StatusFound {
		t.Fatalf("connect = %d %s", rec.Code, rec.Body.String())
	}
	before := s.UserCount()
	st, ck := feishuStartFlow(t, h)
	assertFeishuExistingAccountPage(t, s, feishuCallback(t, h, st, ck, "good"), before, "alice")

	// Follow the guidance: bind sign-in from alice's settings.
	_, state = feishuBindStartAs(t, h, session)
	if rec := feishuConnectCallbackAs(t, h, session, state, "noref-bind"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?feishu=bound#/settings" {
		t.Fatalf("bind = %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	st, ck = feishuStartFlow(t, h)
	rec := feishuCallback(t, h, st, ck, "good")
	if rec.Code != http.StatusFound {
		t.Fatalf("sign-in after bind = %d %s", rec.Code, rec.Body.String())
	}
	if u := signedCookieUser(t, client, rec); u.ID != alice.ID {
		t.Fatalf("signed in as %d want alice %d", u.ID, alice.ID)
	}
	if s.UserCount() != before {
		t.Fatal("no account may be provisioned")
	}
}

// The grant belongs to account A while account B signs in with the identity:
// A's connect+bind is refused with the improved page and A's grant is kept.
func TestFeishuIdentityTakenPageWording(t *testing.T) {
	fake := newFakeFeishu(t)
	h, s, client := newFeishuTestHandler(t, fake.server.URL)
	b, err := s.CreateIdentityAccount(repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三"}, false)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.CreateLocalAccount("alice", "pw-secret-1", false)
	feishuTestGrant(t, s, a.ID, "tenantA", "union-1")
	session := sessionCookie(t, client, a)
	_, state := feishuBindStartAs(t, h, session)
	rec := feishuConnectCallbackAs(t, h, session, state, "noref-bind")
	body := rec.Body.String()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("taken = %d %s", rec.Code, body)
	}
	for _, want := range []string{"<code>" + b.LoginName + "</code>", "另一个平台账号", "如果那是你自己的另一个账号", "用飞书登录那个账号", "联系平台管理员合并"} {
		if !strings.Contains(body, want) {
			t.Fatalf("taken page lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "张三") {
		t.Fatalf("taken page leaks the other account's display name: %s", body)
	}
	if g, ok := s.FeishuGrant(a.ID); !ok || g.Status != repo.FeishuGrantActive || g.Subject != "union-1" {
		t.Fatalf("A's grant changed: %+v %v", g, ok)
	}
	if owner, _ := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); owner.ID != b.ID {
		t.Fatalf("identity moved to %d", owner.ID)
	}
}
