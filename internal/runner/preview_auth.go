package runner

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"agentflow/internal/previewenv"
)

// previewAuth is the forward_auth target of the edge Caddy (GET
// /verify?env=<envId>). It checks gateway credentials locally with the secret
// shared with the control plane; see docs/proposals/compose-preview-environments.md §5.
type previewAuth struct {
	secret []byte
	nonces *previewenv.Nonces
	now    func() time.Time

	mu     sync.Mutex
	access map[string]time.Time // envId -> last authorized request
}

func newPreviewAuth(secret []byte) *previewAuth {
	return &previewAuth{secret: secret, nonces: previewenv.NewNonces(), now: time.Now, access: map[string]time.Time{}}
}

func (a *previewAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/verify" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// Only the env id Caddy wrote into the route counts, never the client's Host.
	envID := r.URL.Query().Get("env")
	now := a.now()
	if cookie, err := r.Cookie(previewenv.CookieName); err == nil && envID != "" &&
		previewenv.VerifyCookie(a.secret, cookie.Value, envID, now) == nil {
		a.touch(envID, now)
		w.WriteHeader(http.StatusOK)
		return
	}
	rest, ticket := previewenv.StripTicket(r.Header.Get("X-Forwarded-Uri"))
	if ticket != "" && envID != "" && previewenv.VerifyTicket(a.secret, ticket, envID, now, a.nonces) == nil {
		value, err := previewenv.MintCookie(a.secret, envID, now)
		if err == nil {
			a.touch(envID, now)
			http.SetCookie(w, &http.Cookie{
				Name: previewenv.CookieName, Value: value, Path: "/",
				MaxAge: int(previewenv.CookieTTL / time.Second), HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			w.Header().Set("Location", previewRedirectPath(rest))
			w.WriteHeader(http.StatusFound)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("preview login required: open it from LoopForge\n"))
}

// previewRedirectPath keeps the 302 on the preview host: X-Forwarded-Uri can
// be forged, and "//evil.com" or "/\\evil.com" would leave it.
func previewRedirectPath(uri string) string {
	if !strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "//") || strings.HasPrefix(uri, "/\\") {
		return "/"
	}
	for _, r := range uri {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	return uri
}

func (a *previewAuth) touch(envID string, now time.Time) {
	a.mu.Lock()
	a.access[envID] = now
	a.mu.Unlock()
}

// LastAccess returns the last authorized request per environment. Not
// reported yet; idle shutdown will read it.
func (a *previewAuth) LastAccess() map[string]time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]time.Time, len(a.access))
	for id, at := range a.access {
		out[id] = at
	}
	return out
}

// PreviewLastAccess exposes the verify listener's last-access times; nil when
// the listener is not running.
func (d *Daemon) PreviewLastAccess() map[string]time.Time {
	if d.previewAuth == nil {
		return nil
	}
	return d.previewAuth.LastAccess()
}

// startPreviewAuth runs the verify listener until ctx ends when
// PreviewAuthAddr is set. A missing or short secret disables it.
func (d *Daemon) startPreviewAuth(ctx context.Context) {
	addr := d.cfg.PreviewAuthAddr
	if addr == "" {
		return
	}
	if len(d.cfg.PreviewSecret) < previewenv.MinSecretLen {
		log.Printf("preview verify listener disabled: LOOPFORGE_PREVIEW_SECRET must be at least %d bytes", previewenv.MinSecretLen)
		return
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("preview verify listener disabled: %v", err)
		return
	}
	d.previewAuth = newPreviewAuth([]byte(d.cfg.PreviewSecret))
	server := &http.Server{Handler: d.previewAuth, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("preview verify listener: %v", err)
		}
	}()
	log.Printf("preview verify listener on %s", listener.Addr())
}
