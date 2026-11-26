package runner

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/previewenv"
)

func TestPreviewAuthVerify(t *testing.T) {
	secret := []byte(strings.Repeat("k", previewenv.MinSecretLen))
	now := time.Unix(1_800_000_000, 0)
	auth := newPreviewAuth(secret)
	auth.now = func() time.Time { return now }
	server := httptest.NewServer(auth)
	defer server.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	verify := func(env, forwardedURI, cookie string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/verify?env="+env, nil)
		if forwardedURI != "" {
			req.Header.Set("X-Forwarded-Uri", forwardedURI)
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: previewenv.CookieName, Value: cookie})
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if resp := verify("pe-a", "/", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential: %d", resp.StatusCode)
	}

	ticket, err := previewenv.MintTicket(secret, "pe-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if resp := verify("pe-b", "/?__lf="+ticket, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ticket for another env: %d", resp.StatusCode)
	}
	resp := verify("pe-a", "/orders?page=2&__lf="+ticket, "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/orders?page=2" {
		t.Fatalf("ticket: %d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	setCookie := resp.Header.Get("Set-Cookie")
	for _, part := range []string{previewenv.CookieName + "=", "Path=/", "HttpOnly", "SameSite=Lax", "Max-Age=28800"} {
		if !strings.Contains(setCookie, part) {
			t.Fatalf("Set-Cookie %q lacks %q", setCookie, part)
		}
	}
	if strings.Contains(setCookie, "Domain=") {
		t.Fatalf("cookie must be host-only: %q", setCookie)
	}
	cookie := resp.Cookies()[0].Value
	if at, ok := auth.LastAccess()["pe-a"]; !ok || !at.Equal(now) {
		t.Fatalf("last access = %v %v", at, ok)
	}

	if resp := verify("pe-a", "/?__lf="+ticket, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed ticket: %d", resp.StatusCode)
	}
	now = now.Add(time.Minute)
	if resp := verify("pe-a", "/assets/app.js", cookie); resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie: %d", resp.StatusCode)
	}
	if at := auth.LastAccess()["pe-a"]; !at.Equal(now) {
		t.Fatalf("cookie did not record access: %v", at)
	}
	if resp := verify("pe-b", "/", cookie); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie for another env: %d", resp.StatusCode)
	}
	if _, ok := auth.LastAccess()["pe-b"]; ok {
		t.Fatal("rejected request recorded access")
	}
	now = now.Add(previewenv.CookieTTL)
	if resp := verify("pe-a", "/", cookie); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired cookie: %d", resp.StatusCode)
	}
	if resp := verify("", "/", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing env: %d", resp.StatusCode)
	}
	other, _ := http.Get(server.URL + "/other")
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Fatalf("other path: %d", other.StatusCode)
	}
}

func TestPreviewAuthRedirectStaysOnHost(t *testing.T) {
	secret := []byte(strings.Repeat("k", previewenv.MinSecretLen))
	auth := newPreviewAuth(secret)
	for _, forwarded := range []string{"//evil.com/x", "/\\evil.com", "https://evil.com/x", "evil.com", "/a\r\nSet-Cookie:x"} {
		ticket, _ := previewenv.MintTicket(secret, "pe-a", time.Now())
		sep := "?"
		if strings.Contains(forwarded, "?") {
			sep = "&"
		}
		req := httptest.NewRequest(http.MethodGet, "/verify?env=pe-a", nil)
		req.Header.Set("X-Forwarded-Uri", forwarded+sep+previewenv.TicketParam+"="+ticket)
		rec := httptest.NewRecorder()
		auth.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
			t.Errorf("%q: %d Location=%q", forwarded, rec.Code, rec.Header().Get("Location"))
		}
	}
	for in, want := range map[string]string{"/": "/", "/orders?page=2#f": "/orders?page=2#f", "/a//b": "/a//b", "": "/", "/\t": "/"} {
		if got := previewRedirectPath(in); got != want {
			t.Errorf("previewRedirectPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStartPreviewAuthNeedsStrongSecret(t *testing.T) {
	d := &Daemon{cfg: Config{PreviewAuthAddr: "127.0.0.1:0", PreviewSecret: "short"}}
	d.startPreviewAuth(t.Context())
	if d.previewAuth != nil || d.PreviewLastAccess() != nil {
		t.Fatal("verify listener started with a short secret")
	}
}
