package repo

import "testing"

func TestFeishuGrantHolders(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	grant := func(userID int64, tenant, subject string) {
		t.Helper()
		if err := s.SaveFeishuGrant(userID, FeishuGrantInput{Tenant: tenant, Subject: subject, Tokens: FeishuTokens{AccessToken: "a", RefreshToken: "r"}}); err != nil {
			t.Fatal(err)
		}
	}
	active, _ := s.CreateLocalAccount("active", "pw-secret-1", false)
	revoked, _ := s.CreateLocalAccount("revoked", "pw-secret-1", false)
	expired, _ := s.CreateLocalAccount("expired", "pw-secret-1", false)
	signsIn, _ := s.CreateLocalAccount("signs-in", "pw-secret-1", false)
	otherTenant, _ := s.CreateLocalAccount("other-tenant", "pw-secret-1", false)
	for _, id := range []int64{active.ID, revoked.ID, expired.ID, signsIn.ID} {
		grant(id, "tenantA", "union-1")
	}
	grant(otherTenant.ID, "tenantB", "union-1")
	if _, err := s.RevokeFeishuGrant(revoked.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&userFeishuGrantRow{}).Where("user_id = ?", expired.ID).Update("status", FeishuGrantExpired).Error; err != nil {
		t.Fatal(err)
	}
	// An account with its own Feishu sign-in identity is excluded.
	if err := s.BindIdentity(signsIn.ID, ExternalIdentity{Provider: IdentityProviderFeishu, Tenant: "tenantA", Subject: "union-9"}); err != nil {
		t.Fatal(err)
	}

	holders, err := s.FeishuGrantHolders(" tenantA ", " union-1 ")
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, u := range holders {
		got = append(got, u.ID)
	}
	want := []int64{active.ID, revoked.ID, expired.ID}
	if len(got) != len(want) {
		t.Fatalf("holders = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("holders = %v want %v (oldest first)", got, want)
		}
	}
	if h, err := s.FeishuGrantHolders("tenantA", ""); err != nil || len(h) != 0 {
		t.Fatalf("empty subject = %v %v", h, err)
	}
	if h, err := s.FeishuGrantHolders("tenantC", "union-1"); err != nil || len(h) != 0 {
		t.Fatalf("other tenant = %v %v", h, err)
	}

	accounts, err := s.ListPlatformAccounts()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		if a.FeishuLogin != (a.UserID == signsIn.ID) {
			t.Fatalf("FeishuLogin of %s = %v", a.LoginName, a.FeishuLogin)
		}
	}
}
