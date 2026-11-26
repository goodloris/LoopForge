// Package previewenv holds the gateway credentials for compose preview
// environments: one-time tickets that open a preview and the per-host cookie
// that keeps it open. The control plane mints tickets; the preview runner
// verifies tickets and cookies locally with the same shared secret, so no
// request calls back to the control plane.
//
// Formats (docs/proposals/compose-preview-environments.md §5), with
// b64url = base64.RawURLEncoding and the HMAC over the raw payload:
//
//	ticket = b64url(envId "|" expUnix "|" nonce16B) "." b64url(HMAC-SHA256(secret, "ticket|" + payload))
//	cookie = b64url(envId "|" expUnix)              "." b64url(HMAC-SHA256(secret, "cookie|" + payload))
//
// All functions take the current time as a parameter.
package previewenv

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	TicketTTL    = 120 * time.Second
	CookieTTL    = 8 * time.Hour
	CookieName   = "lf_pv"
	TicketParam  = "__lf"
	MinSecretLen = 32
	nonceLen     = 16
)

var (
	ErrWeakSecret  = errors.New("preview secret must be at least 32 bytes")
	ErrMalformed   = errors.New("preview credential is malformed")
	ErrBadSig      = errors.New("preview credential signature is invalid")
	ErrExpired     = errors.New("preview credential has expired")
	ErrEnvMismatch = errors.New("preview credential belongs to another environment")
	ErrReplayed    = errors.New("preview ticket was already used")
)

// MintTicket returns a one-time ticket for envID valid for TicketTTL.
func MintTicket(secret []byte, envID string, now time.Time) (string, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return sign(secret, "ticket", envID, expString(now, TicketTTL)+"|"+string(nonce))
}

// VerifyTicket accepts a ticket for envID once: a valid ticket is recorded in
// used until it expires, and a second presentation returns ErrReplayed.
func VerifyTicket(secret []byte, ticket, envID string, now time.Time, used *Nonces) error {
	if used == nil {
		return errors.New("preview ticket verification needs a nonce set")
	}
	fields, exp, err := verify(secret, "ticket", ticket, envID, now, 3)
	if err != nil {
		return err
	}
	if len(fields[2]) != nonceLen {
		return ErrMalformed
	}
	if !used.Use(fields[2], exp, now) {
		return ErrReplayed
	}
	return nil
}

// MintCookie returns the lf_pv cookie value for envID valid for CookieTTL.
func MintCookie(secret []byte, envID string, now time.Time) (string, error) {
	return sign(secret, "cookie", envID, expString(now, CookieTTL))
}

// VerifyCookie checks an lf_pv cookie value for envID.
func VerifyCookie(secret []byte, cookie, envID string, now time.Time) error {
	_, _, err := verify(secret, "cookie", cookie, envID, now, 2)
	return err
}

func expString(now time.Time, ttl time.Duration) string {
	return strconv.FormatInt(now.Add(ttl).Unix(), 10)
}

func mac(secret []byte, purpose, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(purpose + "|" + payload))
	return h.Sum(nil)
}

func sign(secret []byte, purpose, envID, rest string) (string, error) {
	if len(secret) < MinSecretLen {
		return "", ErrWeakSecret
	}
	if envID == "" || strings.Contains(envID, "|") {
		return "", ErrMalformed
	}
	payload := envID + "|" + rest
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(mac(secret, purpose, payload)), nil
}

// verify checks the signature first, then the shape, expiry and envID. The
// envID and expiry fields never contain "|", so a raw nonce may.
func verify(secret []byte, purpose, token, envID string, now time.Time, parts int) ([]string, int64, error) {
	if len(secret) < MinSecretLen {
		return nil, 0, ErrWeakSecret
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, 0, ErrMalformed
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(body)
	if err != nil {
		return nil, 0, ErrMalformed
	}
	got, err := enc.DecodeString(sig)
	if err != nil {
		return nil, 0, ErrMalformed
	}
	if !hmac.Equal(got, mac(secret, purpose, string(payload))) {
		return nil, 0, ErrBadSig
	}
	fields := strings.SplitN(string(payload), "|", parts)
	if len(fields) != parts {
		return nil, 0, ErrMalformed
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return nil, 0, ErrMalformed
	}
	if now.Unix() > exp {
		return nil, 0, ErrExpired
	}
	if !hmac.Equal([]byte(fields[0]), []byte(envID)) {
		return nil, 0, ErrEnvMismatch
	}
	return fields, exp, nil
}

// Nonces is a concurrency-safe set of used ticket nonces, each kept until its
// ticket expires.
type Nonces struct {
	mu   sync.Mutex
	seen map[string]int64 // nonce -> expUnix
}

func NewNonces() *Nonces { return &Nonces{seen: map[string]int64{}} }

// Use records nonce until expUnix and reports whether it was unused.
// ponytail: every call scans the set to drop expired nonces; fine for
// 120-second tickets opened by people, switch to a time-ordered queue if
// ticket volume ever grows.
func (n *Nonces) Use(nonce string, expUnix int64, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	nowUnix := now.Unix()
	for key, exp := range n.seen {
		if nowUnix > exp {
			delete(n.seen, key)
		}
	}
	if _, ok := n.seen[nonce]; ok {
		return false
	}
	n.seen[nonce] = expUnix
	return true
}

// Len reports how many nonces are still held.
func (n *Nonces) Len() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.seen)
}

// StripTicket removes every __lf parameter from a request URI (path plus
// optional query, as in X-Forwarded-Uri) and returns the rest together with
// the first ticket value. Other parameters keep their order and encoding; a
// query left empty is dropped, and an empty path becomes "/".
func StripTicket(uri string) (rest, ticket string) {
	base, fragment, hasFragment := strings.Cut(uri, "#")
	path, query, hasQuery := strings.Cut(base, "?")
	if path == "" {
		path = "/"
	}
	if hasQuery {
		kept := make([]string, 0, strings.Count(query, "&")+1)
		found := false
		for _, pair := range strings.Split(query, "&") {
			rawKey, rawValue, _ := strings.Cut(pair, "=")
			if key, err := url.QueryUnescape(rawKey); err == nil && key == TicketParam {
				if !found {
					ticket, _ = url.QueryUnescape(rawValue)
					found = true
				}
				continue
			}
			if pair != "" {
				kept = append(kept, pair)
			}
		}
		if len(kept) > 0 {
			path += "?" + strings.Join(kept, "&")
		}
	}
	if hasFragment {
		path += "#" + fragment
	}
	return path, ticket
}
