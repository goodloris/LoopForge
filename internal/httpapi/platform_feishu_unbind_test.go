package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/repo"
)

// Administrators can unbind an account's Feishu sign-in from 平台管理 →
// 成员账号, but never leave an account without any way to sign in.
func TestPlatformAccountUnbindFeishuLogin(t *testing.T) {
	s, client, owner, member := platformSettingsFixture(t)
	const origin = "http://localhost"
	alice, err := s.CreateLocalAccount("alice", "alice-password", false)
	if err != nil {
		t.Fatal(err)
	}
	feishuID := repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三"}
	if err := s.BindIdentity(alice.ID, feishuID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFeishuGrant(alice.ID, repo.FeishuGrantInput{Tenant: "tenantA", Subject: "union-1", Tokens: repo.FeishuTokens{AccessToken: "acc", RefreshToken: "ref"}}); err != nil {
		t.Fatal(err)
	}
	feishuOnly, err := s.CreateIdentityAccount(repo.ExternalIdentity{Provider: repo.IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-2"}, false)
	if err != nil {
		t.Fatal(err)
	}

	// The list reports which accounts sign in with Feishu.
	w := client.call(owner, "GET", "/api/platform/accounts", "", "")
	var listed struct{ Accounts []repo.PlatformAccount }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	byLogin := map[string]repo.PlatformAccount{}
	for _, a := range listed.Accounts {
		byLogin[a.LoginName] = a
	}
	if !byLogin["alice"].FeishuLogin || !byLogin[feishuOnly.LoginName].FeishuLogin || byLogin["owner"].FeishuLogin {
		t.Fatalf("feishuLogin flags = %+v", listed.Accounts)
	}

	path := "/api/platform/accounts/" + itoa(alice.ID) + "/feishu-login"
	if w := client.call(member, "DELETE", path, origin, ""); w.Code != 403 {
		t.Fatalf("member unbind: %d", w.Code)
	}
	if w := client.call(owner, "DELETE", path, "https://attacker.invalid", ""); w.Code != 403 {
		t.Fatalf("cross-origin unbind: %d", w.Code)
	}
	if w := client.call(owner, "DELETE", "/api/platform/accounts/abc/feishu-login", origin, ""); w.Code != 400 {
		t.Fatalf("bad id: %d", w.Code)
	}
	if w := client.call(owner, "DELETE", "/api/platform/accounts/999999/feishu-login", origin, ""); w.Code != 404 {
		t.Fatalf("missing account: %d", w.Code)
	}
	owners, _ := s.UserByLoginName("owner")
	if w := client.call(owner, "DELETE", "/api/platform/accounts/"+itoa(owners.ID)+"/feishu-login", origin, ""); w.Code != 404 || !strings.Contains(w.Body.String(), "没有绑定飞书登录") {
		t.Fatalf("not bound: %d %s", w.Code, w.Body.String())
	}

	// The only sign-in method is kept.
	onlyPath := "/api/platform/accounts/" + itoa(feishuOnly.ID) + "/feishu-login"
	if w := client.call(owner, "DELETE", onlyPath, origin, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "唯一的登录方式") {
		t.Fatalf("last sign-in method: %d %s", w.Code, w.Body.String())
	}
	if u, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-2"); !ok || u.ID != feishuOnly.ID {
		t.Fatal("refused unbind must keep the identity")
	}

	w = client.call(owner, "DELETE", path, origin, "")
	var account repo.PlatformAccount
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &account) != nil || account.UserID != alice.ID || account.FeishuLogin || !account.Password {
		t.Fatalf("unbind: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.UserByIdentity(repo.IdentityProviderFeishu, "tenantA", "union-1"); ok {
		t.Fatal("unbind must remove the sign-in identity")
	}
	if g, ok := s.FeishuGrant(alice.ID); !ok || g.Status != repo.FeishuGrantActive {
		t.Fatalf("unbind must keep the agents' grant: %+v %v", g, ok)
	}
	if !s.CheckLocalPassword(alice.ID, "alice-password") {
		t.Fatal("password changed")
	}
	if w := client.call(owner, "DELETE", path, origin, ""); w.Code != 404 {
		t.Fatalf("second unbind: %d", w.Code)
	}
}
