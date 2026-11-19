package repo

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestFeishuIdentityProvisionAndLookup(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// A password account must never be reachable through the Feishu identity.
	if _, err := s.CreateLocalAccount("admin", "pw-secret", true); err != nil {
		t.Fatal(err)
	}

	id := FeishuIdentity{Subject: "union-1", TenantKey: "tenantA", Name: "张三", AvatarURL: "https://feishu.example/a.png"}
	created, err := s.CreateFeishuAccount(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if created.ChineseName != "张三" {
		t.Fatalf("chinese name = %q", created.ChineseName)
	}
	if created.LoginName == "admin" || created.LoginName == "" {
		t.Fatalf("generated login name must not clash with a password account: %q", created.LoginName)
	}
	if !strings.HasPrefix(created.LoginName, "feishu-") {
		t.Fatalf("login name should be a generated feishu name: %q", created.LoginName)
	}

	// Lookup by the exact (subject, tenant) pair.
	found, ok := s.UserByFeishuIdentity("union-1", "tenantA")
	if !ok || found.ID != created.ID {
		t.Fatalf("lookup by identity: %+v %v", found, ok)
	}
	// Same subject, different tenant must not match.
	if _, ok := s.UserByFeishuIdentity("union-1", "tenantB"); ok {
		t.Fatal("identity matched across tenants")
	}
	// Empty subject or tenant never matches.
	if _, ok := s.UserByFeishuIdentity("", "tenantA"); ok {
		t.Fatal("empty subject matched")
	}
	if _, ok := s.UserByFeishuIdentity("union-1", ""); ok {
		t.Fatal("empty tenant matched")
	}

	// The Feishu identity must not adopt the password account by name.
	if admin, ok := s.UserByLoginName("admin"); !ok || admin.ID == created.ID {
		t.Fatalf("password account was taken over: admin=%+v created=%d", admin, created.ID)
	}

	// A second member provisions a distinct account.
	second, err := s.CreateFeishuAccount(FeishuIdentity{Subject: "open-2", TenantKey: "tenantA", Name: "李四"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == created.ID || second.LoginName == created.LoginName {
		t.Fatalf("second account collided: %+v", second)
	}

	// Re-provisioning the same identity returns the existing account.
	again, err := s.CreateFeishuAccount(id, false)
	if err != nil || again.ID != created.ID {
		t.Fatalf("re-provision returned a new account: %+v %v", again, err)
	}

	// A refresh updates the stored name and avatar without rebinding.
	if err := s.RefreshFeishuProfile(created.ID, "张三丰", "https://feishu.example/b.png"); err != nil {
		t.Fatal(err)
	}
	if refreshed, _ := s.User(created.ID); refreshed.ChineseName != "张三丰" {
		t.Fatalf("refresh name = %q", refreshed.ChineseName)
	}
}

func TestFeishuFirstAccountBecomesAdmin(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	admin, err := s.CreateFeishuAccount(FeishuIdentity{Subject: "boss", TenantKey: "tenantA", Name: "老板"}, true)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := s.User(admin.ID)
	if !ok || !domain.IsPlatformAdmin(stored) {
		t.Fatalf("first feishu account should be platform admin: %+v %v", stored, ok)
	}
}
