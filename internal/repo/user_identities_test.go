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

	id := ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三", AvatarURL: "https://feishu.example/a.png"}
	created, err := s.CreateIdentityAccount(id, false)
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
	found, ok := s.UserByIdentity(IdentityProviderFeishu, "tenantA", "union-1")
	if !ok || found.ID != created.ID {
		t.Fatalf("lookup by identity: %+v %v", found, ok)
	}
	// Same subject, different tenant must not match.
	if _, ok := s.UserByIdentity(IdentityProviderFeishu, "tenantB", "union-1"); ok {
		t.Fatal("identity matched across tenants")
	}
	// An empty subject never matches; an empty tenant is a different tenant.
	if _, ok := s.UserByIdentity(IdentityProviderFeishu, "tenantA", ""); ok {
		t.Fatal("empty subject matched")
	}
	if _, ok := s.UserByIdentity(IdentityProviderFeishu, "", "union-1"); ok {
		t.Fatal("empty tenant matched")
	}

	// The Feishu identity must not adopt the password account by name.
	if admin, ok := s.UserByLoginName("admin"); !ok || admin.ID == created.ID {
		t.Fatalf("password account was taken over: admin=%+v created=%d", admin, created.ID)
	}

	// A second member provisions a distinct account.
	second, err := s.CreateIdentityAccount(ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "tenantA", Subject: "open-2", Name: "李四"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == created.ID || second.LoginName == created.LoginName {
		t.Fatalf("second account collided: %+v", second)
	}

	// Re-provisioning the same identity returns the existing account.
	again, err := s.CreateIdentityAccount(id, false)
	if err != nil || again.ID != created.ID {
		t.Fatalf("re-provision returned a new account: %+v %v", again, err)
	}

	// A refresh updates the stored name and avatar without rebinding.
	if err := s.RecordIdentitySignIn(ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三丰", AvatarURL: "https://feishu.example/b.png"}); err != nil {
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

	admin, err := s.CreateIdentityAccount(ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "tenantA", Subject: "boss", Name: "老板"}, true)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := s.User(admin.ID)
	if !ok || !domain.IsPlatformAdmin(stored) {
		t.Fatalf("first feishu account should be platform admin: %+v %v", stored, ok)
	}
}

func TestIdentityKeyIsProviderTenantSubject(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	feishu, err := s.CreateIdentityAccount(ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "t1", Subject: "same"}, false)
	if err != nil {
		t.Fatal(err)
	}
	// The same subject from another provider, with no tenant, is another member.
	other, err := s.CreateIdentityAccount(ExternalIdentity{Provider: "gitlab", Subject: "same"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == feishu.ID || !strings.HasPrefix(other.LoginName, "gitlab-") {
		t.Fatalf("providers share an identity: feishu=%+v other=%+v", feishu, other)
	}
	if got, ok := s.UserByIdentity("gitlab", "", "same"); !ok || got.ID != other.ID {
		t.Fatalf("tenant-less lookup: %+v %v", got, ok)
	}
	if _, ok := s.UserByIdentity("", "t1", "same"); ok {
		t.Fatal("empty provider matched")
	}
	if err := s.RecordIdentitySignIn(ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "t2", Subject: "same"}); err == nil {
		t.Fatal("recording an unbound identity should fail")
	}
}
