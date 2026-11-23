package repo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func feishuGrantStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	user, err := s.CreateLocalAccount("grantee", "pw-secret-1", false)
	if err != nil {
		t.Fatal(err)
	}
	return s, user.ID
}

func saveTestFeishuGrant(t *testing.T, s *Store, userID int64, accessTTL time.Duration) {
	t.Helper()
	now := time.Now()
	if err := s.SaveFeishuGrant(userID, FeishuGrantInput{
		Tenant: "tenantA", Subject: "union-1", OpenID: "open-1", Name: "张三", Preset: "readonly",
		Tokens: FeishuTokens{AccessToken: "u-1", AccessExpiresAt: now.Add(accessTTL), RefreshToken: "r-1", RefreshExpiresAt: now.Add(7 * 24 * time.Hour), Scopes: []string{"offline_access", "docx:document:readonly"}},
	}); err != nil {
		t.Fatal(err)
	}
}

// rotatingRefresh mimics Feishu: each refresh token works once and yields the
// next one.
func rotatingRefresh(calls *int32, delay time.Duration) FeishuRefreshFunc {
	var mu sync.Mutex
	used := map[string]bool{}
	return func(ctx context.Context, refreshToken string) (FeishuTokens, error) {
		atomic.AddInt32(calls, 1)
		time.Sleep(delay)
		mu.Lock()
		defer mu.Unlock()
		if used[refreshToken] {
			return FeishuTokens{}, fmt.Errorf("code 20073: %w", ErrFeishuGrantRejected)
		}
		used[refreshToken] = true
		n := atomic.LoadInt32(calls)
		return FeishuTokens{AccessToken: fmt.Sprintf("u-%d", n+1), AccessExpiresAt: time.Now().Add(2 * time.Hour), RefreshToken: fmt.Sprintf("r-%d", n+1), RefreshExpiresAt: time.Now().Add(7 * 24 * time.Hour)}, nil
	}
}

func TestFeishuUserTokenReusesFreshToken(t *testing.T) {
	s, uid := feishuGrantStore(t)
	saveTestFeishuGrant(t, s, uid, time.Hour)
	var calls int32
	got, err := s.FeishuUserToken(context.Background(), uid, 30*time.Minute, rotatingRefresh(&calls, 0))
	if err != nil || got.Token != "u-1" || got.Name != "张三" || got.Preset != "readonly" {
		t.Fatalf("token = %+v err=%v", got, err)
	}
	if calls != 0 {
		t.Fatalf("a token with an hour left must not refresh, got %d calls", calls)
	}
}

func TestFeishuUserTokenRefreshesAndRotates(t *testing.T) {
	s, uid := feishuGrantStore(t)
	saveTestFeishuGrant(t, s, uid, 10*time.Minute)
	var calls int32
	refresh := rotatingRefresh(&calls, 0)
	got, err := s.FeishuUserToken(context.Background(), uid, 30*time.Minute, refresh)
	if err != nil || got.Token != "u-2" {
		t.Fatalf("refreshed token = %+v err=%v", got, err)
	}
	// The rotated refresh token was written back: a forced second refresh uses
	// r-2, which the fake accepts, instead of the spent r-1.
	got, err = s.FeishuUserToken(context.Background(), uid, 3*time.Hour, refresh)
	if err != nil || got.Token != "u-3" || calls != 2 {
		t.Fatalf("second refresh = %+v calls=%d err=%v", got, calls, err)
	}
	if g, _ := s.FeishuGrant(uid); g.Status != FeishuGrantActive || len(g.Scopes) != 2 {
		t.Fatalf("grant after refresh = %+v (scopes are kept when the refresh omits them)", g)
	}
}

func TestFeishuUserTokenConcurrentRoundsRefreshOnce(t *testing.T) {
	s, uid := feishuGrantStore(t)
	saveTestFeishuGrant(t, s, uid, time.Minute)
	stores := []*Store{s}
	if s.isPostgres() {
		// A second instance on the same rows: only the row lock serializes it.
		peer, err := OpenTestPeer(s)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(peer.Close)
		stores = append(stores, peer)
	}
	var calls int32
	refresh := rotatingRefresh(&calls, 150*time.Millisecond)
	var wg sync.WaitGroup
	tokens := make([]string, 4)
	errs := make([]error, 4)
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := stores[i%len(stores)].FeishuUserToken(context.Background(), uid, 30*time.Minute, refresh)
			tokens[i], errs[i] = got.Token, err
		}(i)
	}
	wg.Wait()
	for i := range tokens {
		if errs[i] != nil || tokens[i] != "u-2" {
			t.Fatalf("round %d got %q err=%v (all rounds must share one refresh)", i, tokens[i], errs[i])
		}
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
}

func TestFeishuUserTokenRejectedRefreshExpiresGrant(t *testing.T) {
	s, uid := feishuGrantStore(t)
	saveTestFeishuGrant(t, s, uid, time.Minute)
	rejected := func(context.Context, string) (FeishuTokens, error) {
		return FeishuTokens{}, fmt.Errorf("code 20037: %w", ErrFeishuGrantRejected)
	}
	if _, err := s.FeishuUserToken(context.Background(), uid, 30*time.Minute, rejected); !errors.Is(err, ErrFeishuNotConnected) {
		t.Fatalf("rejected refresh err = %v", err)
	}
	g, _ := s.FeishuGrant(uid)
	if g.Status != FeishuGrantExpired {
		t.Fatalf("status = %q want expired", g.Status)
	}
	var calls int32
	if _, err := s.FeishuUserToken(context.Background(), uid, 0, rotatingRefresh(&calls, 0)); !errors.Is(err, ErrFeishuNotConnected) || calls != 0 {
		t.Fatalf("expired grant must not refresh again: err=%v calls=%d", err, calls)
	}
}

func TestFeishuUserTokenTransientErrorKeepsGrant(t *testing.T) {
	s, uid := feishuGrantStore(t)
	saveTestFeishuGrant(t, s, uid, time.Minute)
	down := func(context.Context, string) (FeishuTokens, error) {
		return FeishuTokens{}, errors.New("feishu returned HTTP 503")
	}
	if _, err := s.FeishuUserToken(context.Background(), uid, 30*time.Minute, down); err == nil || errors.Is(err, ErrFeishuNotConnected) {
		t.Fatalf("transient err = %v", err)
	}
	if g, _ := s.FeishuGrant(uid); g.Status != FeishuGrantActive {
		t.Fatalf("a network failure must keep the grant active, got %q", g.Status)
	}
	var calls int32
	if got, err := s.FeishuUserToken(context.Background(), uid, 30*time.Minute, rotatingRefresh(&calls, 0)); err != nil || got.Token != "u-2" {
		t.Fatalf("retry after outage = %+v err=%v (refresh token r-1 must be intact)", got, err)
	}
}

func TestFeishuGrantRevoke(t *testing.T) {
	s, uid := feishuGrantStore(t)
	saveTestFeishuGrant(t, s, uid, time.Hour)
	refresh, err := s.RevokeFeishuGrant(uid)
	if err != nil || refresh != "r-1" {
		t.Fatalf("revoke = %q %v", refresh, err)
	}
	g, ok := s.FeishuGrant(uid)
	if !ok || g.Status != FeishuGrantRevoked || !g.AccessExpiresAt.IsZero() {
		t.Fatalf("grant after revoke = %+v %v", g, ok)
	}
	var calls int32
	if _, err := s.FeishuUserToken(context.Background(), uid, 0, rotatingRefresh(&calls, 0)); !errors.Is(err, ErrFeishuNotConnected) {
		t.Fatalf("revoked grant token err = %v", err)
	}
	// Reconnecting replaces the revoked row.
	saveTestFeishuGrant(t, s, uid, time.Hour)
	if g, _ := s.FeishuGrant(uid); g.Status != FeishuGrantActive {
		t.Fatalf("reconnect status = %q", g.Status)
	}
	// Revoking a member without a grant is a no-op.
	if refresh, err := s.RevokeFeishuGrant(uid + 1000); err != nil || refresh != "" {
		t.Fatalf("revoke without grant = %q %v", refresh, err)
	}
}
