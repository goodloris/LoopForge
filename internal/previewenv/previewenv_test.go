package previewenv

import (
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	secret = []byte(strings.Repeat("s", MinSecretLen))
	t0     = time.Unix(1_800_000_000, 0)
)

func TestTicketRoundTripIsOneTime(t *testing.T) {
	ticket, err := MintTicket(secret, "pe-abc", t0)
	if err != nil {
		t.Fatal(err)
	}
	used := NewNonces()
	if err := VerifyTicket(secret, ticket, "pe-abc", t0.Add(TicketTTL), used); err != nil {
		t.Fatalf("fresh ticket rejected: %v", err)
	}
	if err := VerifyTicket(secret, ticket, "pe-abc", t0.Add(time.Second), used); !errors.Is(err, ErrReplayed) {
		t.Fatalf("replay = %v, want ErrReplayed", err)
	}
	other, _ := MintTicket(secret, "pe-abc", t0)
	if other == ticket {
		t.Fatal("tickets must carry distinct nonces")
	}
}

func TestTicketRejections(t *testing.T) {
	ticket, _ := MintTicket(secret, "pe-abc", t0)
	body, sig, _ := strings.Cut(ticket, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(body)
	forged := base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(payload), "pe-abc", "pe-xyz", 1))) + "." + sig
	cookie, _ := MintCookie(secret, "pe-abc", t0)
	otherSecret := []byte(strings.Repeat("o", MinSecretLen))
	cases := []struct {
		name   string
		secret []byte
		ticket string
		env    string
		now    time.Time
		want   error
	}{
		{"expired", secret, ticket, "pe-abc", t0.Add(TicketTTL + time.Second), ErrExpired},
		{"wrong env", secret, ticket, "pe-xyz", t0, ErrEnvMismatch},
		{"tampered payload", secret, forged, "pe-xyz", t0, ErrBadSig},
		{"tampered sig", secret, body + "." + sig[:len(sig)-2] + "AA", "pe-abc", t0, ErrBadSig},
		{"other secret", otherSecret, ticket, "pe-abc", t0, ErrBadSig},
		{"cookie as ticket", secret, cookie, "pe-abc", t0, ErrBadSig},
		{"no dot", secret, body, "pe-abc", t0, ErrMalformed},
		{"bad base64", secret, "!!." + sig, "pe-abc", t0, ErrMalformed},
		{"weak secret", []byte("short"), ticket, "pe-abc", t0, ErrWeakSecret},
		{"empty env", secret, ticket, "", t0, ErrMalformed},
	}
	for _, tc := range cases {
		if err := VerifyTicket(tc.secret, tc.ticket, tc.env, tc.now, NewNonces()); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := VerifyTicket(secret, ticket, "pe-abc", t0, nil); err == nil {
		t.Error("nil nonce set must be rejected")
	}
}

func TestCookieRoundTripAndRejections(t *testing.T) {
	cookie, err := MintCookie(secret, "pe-abc", t0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // cookies are reusable
		if err := VerifyCookie(secret, cookie, "pe-abc", t0.Add(CookieTTL)); err != nil {
			t.Fatalf("cookie rejected: %v", err)
		}
	}
	ticket, _ := MintTicket(secret, "pe-abc", t0)
	for name, tc := range map[string]struct {
		cookie, env string
		now         time.Time
		want        error
	}{
		"expired":          {cookie, "pe-abc", t0.Add(CookieTTL + time.Second), ErrExpired},
		"wrong env":        {cookie, "pe-xyz", t0, ErrEnvMismatch},
		"ticket as cookie": {ticket, "pe-abc", t0, ErrBadSig},
		"empty":            {"", "pe-abc", t0, ErrMalformed},
		"empty env":        {cookie, "", t0, ErrMalformed},
	} {
		if err := VerifyCookie(secret, tc.cookie, tc.env, tc.now); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestMintRejectsBadInput(t *testing.T) {
	if _, err := MintCookie([]byte("short"), "pe-abc", t0); !errors.Is(err, ErrWeakSecret) {
		t.Errorf("weak secret = %v", err)
	}
	for _, env := range []string{"", "pe|x"} {
		if _, err := MintTicket(secret, env, t0); !errors.Is(err, ErrMalformed) {
			t.Errorf("env %q = %v", env, err)
		}
	}
}

func TestNoncesExpireAndAreConcurrencySafe(t *testing.T) {
	n := NewNonces()
	exp := t0.Add(TicketTTL).Unix()
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n.Use("same", exp, t0) {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("concurrent uses won %d times, want 1", wins)
	}
	n.Use("other", exp, t0)
	if !n.Use("fresh", exp+500, t0.Add(TicketTTL+time.Second)) || n.Len() != 1 {
		t.Fatalf("expired nonces not purged: len=%d", n.Len())
	}
}

func TestStripTicket(t *testing.T) {
	for _, tc := range []struct{ in, rest, ticket string }{
		{"/?__lf=abc", "/", "abc"},
		{"/app?a=1&__lf=abc&b=2", "/app?a=1&b=2", "abc"},
		{"/app?b=2&a=%20x&__lf=abc", "/app?b=2&a=%20x", "abc"},
		{"/app?__lf=one&__lf=two&x", "/app?x", "one"},
		{"/app?%5F%5Flf=abc", "/app", "abc"},
		{"/app?__lf", "/app", ""},
		{"/app?__lfx=1", "/app?__lfx=1", ""},
		{"/app?a=1&&__lf=t", "/app?a=1", "t"},
		{"/app?__lf=t#frag", "/app#frag", "t"},
		{"/app", "/app", ""},
		{"/app?", "/app", ""},
		{"?__lf=t&a=1", "/?a=1", "t"},
		{"", "/", ""},
	} {
		rest, ticket := StripTicket(tc.in)
		if rest != tc.rest || ticket != tc.ticket {
			t.Errorf("StripTicket(%q) = %q, %q; want %q, %q", tc.in, rest, ticket, tc.rest, tc.ticket)
		}
	}
}
