package repo

import (
	"errors"
	"testing"
)

func TestBindIdentityToPasswordAccount(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	admin, err := s.CreateLocalAccount("admin", "pw-secret-1", true)
	if err != nil {
		t.Fatal(err)
	}
	id := ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-1", Name: "张三"}
	if err := s.BindIdentity(admin.ID, id); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.UserByIdentity(IdentityProviderFeishu, "tenantA", "union-1"); !ok || got.ID != admin.ID {
		t.Fatalf("bound identity resolves to %+v %v", got, ok)
	}
	// Binding the same identity again is a no-op.
	if err := s.BindIdentity(admin.ID, id); err != nil {
		t.Fatalf("rebind = %v", err)
	}
	// A second, different Feishu identity on the same account is refused.
	if err := s.BindIdentity(admin.ID, ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-2"}); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("second identity = %v", err)
	}
	// Another account cannot take it: no merge, no move.
	other, _ := s.CreateLocalAccount("bob", "pw-secret-2", false)
	if err := s.BindIdentity(other.ID, id); !errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("taken identity = %v", err)
	}
	if got, _ := s.UserByIdentity(IdentityProviderFeishu, "tenantA", "union-1"); got.ID != admin.ID {
		t.Fatalf("identity moved to %d", got.ID)
	}
	// The password account keeps its password and can unbind.
	if !s.HasOtherSignIn(admin.ID, IdentityProviderFeishu) {
		t.Fatal("a password account has another sign-in method")
	}
	if err := s.UnbindIdentity(admin.ID, IdentityProviderFeishu); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.UserByIdentity(IdentityProviderFeishu, "tenantA", "union-1"); ok {
		t.Fatal("unbind left the identity")
	}
	if err := s.UnbindIdentity(admin.ID, IdentityProviderFeishu); !errors.Is(err, ErrIdentityNotBound) {
		t.Fatalf("unbind twice = %v", err)
	}
}

func TestUnbindKeepsLastSignInMethod(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	// An auto-provisioned Feishu account has no password: its only way in.
	member, err := s.CreateIdentityAccount(ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-3"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.HasOtherSignIn(member.ID, IdentityProviderFeishu) {
		t.Fatal("a Feishu-only account has no other sign-in")
	}
	if err := s.UnbindIdentity(member.ID, IdentityProviderFeishu); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("unbind last method = %v", err)
	}
	if _, ok := s.UserByIdentity(IdentityProviderFeishu, "tenantA", "union-3"); !ok {
		t.Fatal("refused unbind must keep the identity")
	}
	// After setting a password the account may unbind.
	if err := s.SetLocalPassword(member.ID, "pw-secret-3"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnbindIdentity(member.ID, IdentityProviderFeishu); err != nil {
		t.Fatalf("unbind with a password = %v", err)
	}
}
