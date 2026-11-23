package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// Connecting Feishu (飞书) lets agents in a conversation workspace call
// lark-cli as the member who sent the round. The member authorizes once in
// personal settings; the platform keeps the refresh token and only ever hands
// a short-lived user_access_token to a round. The OAuth flow shares the
// sign-in callback (feishuCallback dispatches on the state's purpose), so the
// Feishu app registers a single redirect URL.
//
// A connection is not a sign-in: a password account may connect without
// gaining a Feishu login, and the connected identity is recorded on the grant
// only. An account that signs in with Feishu may connect only that same
// identity, and an identity another account signs in with is refused.

// feishuScopePresets are the permission levels a member picks when
// connecting; offline_access is always added so Feishu returns a refresh
// token. Every scope must be enabled for the app in the Feishu developer
// console, or the authorization page fails with error 20027. The lists follow
// the APIs the lark-* skills call through lark-cli.
var feishuScopePresets = map[string][]string{
	"readonly": {
		"docx:document:readonly", "wiki:wiki:readonly", "drive:drive:readonly", "sheets:spreadsheet:readonly",
		"calendar:calendar:readonly", "im:chat:readonly", "im:message:readonly",
		"search:docs:read", "search:message", "contact:user.base:readonly",
	},
	"readwrite": {
		"docx:document:readonly", "wiki:wiki:readonly", "drive:drive:readonly", "sheets:spreadsheet:readonly",
		"calendar:calendar:readonly", "im:chat:readonly", "im:message:readonly",
		"search:docs:read", "search:message", "contact:user.base:readonly",
		"docx:document", "wiki:wiki", "drive:drive", "sheets:spreadsheet", "calendar:calendar",
		"im:message", "im:message.send_as_user",
	},
}

const (
	feishuDefaultPreset = "readonly"
	feishuOfflineScope  = "offline_access"
	// feishuRoundMinValid is how long an injected user token must still be
	// valid when a round starts; a shorter remainder is refreshed first.
	feishuRoundMinValid = 30 * time.Minute
	// feishuTenantTokenLead refreshes the cached tenant token this long
	// before it expires.
	feishuTenantTokenLead = 5 * time.Minute
)

func feishuPresetScopes(preset string) []string {
	return append([]string{feishuOfflineScope}, feishuScopePresets[preset]...)
}

// Feishu OAuth error codes that reject a refresh token for good: invalid,
// expired, revoked, already used, or the member/app is no longer usable.
// Anything else (20050 server error, 20072 unavailable, network) is
// transient and keeps the grant.
var feishuRefreshRejectedCodes = map[int]bool{
	20008: true, 20009: true, 20010: true, 20024: true, 20026: true, 20037: true,
	20048: true, 20064: true, 20066: true, 20069: true, 20073: true,
}

// Authorization-code errors on the v2 token endpoint: not found, expired,
// PKCE failure, already used.
var feishuCodeRejectedCodes = map[int]bool{20003: true, 20004: true, 20049: true, 20065: true}

// ---- Feishu OAuth client (open-apis/authen/v2/oauth/token) ----

type feishuOAuthError struct{ code int }

func (e *feishuOAuthError) Error() string { return fmt.Sprintf("feishu oauth failed: code %d", e.code) }

// oauthToken posts a v2 token request. The error never carries the request
// or response body, so secrets, codes and tokens stay out of logs.
func (c *feishuClient) oauthToken(ctx context.Context, form url.Values) (repo.FeishuTokens, error) {
	form.Set("client_id", c.appID)
	form.Set("client_secret", c.appSecret)
	var out struct {
		Code             int    `json:"code"`
		AccessToken      string `json:"access_token"`
		ExpiresIn        int64  `json:"expires_in"`
		RefreshToken     string `json:"refresh_token"`
		RefreshExpiresIn int64  `json:"refresh_token_expires_in"`
		Scope            string `json:"scope"`
	}
	// The documented pair of /open-apis/authen/v1/authorize: a JSON POST to
	// open.feishu.cn. accounts.feishu.cn/oauth/v3/token answers a code issued
	// there with 20049 (PKCE check failed) even when the verifier matches.
	body := map[string]string{}
	for key := range form {
		body[key] = form.Get(key)
	}
	status, err := c.postJSON(ctx, c.baseURL+"/open-apis/authen/v2/oauth/token", body, &out)
	if err != nil {
		return repo.FeishuTokens{}, err
	}
	if out.Code != 0 {
		return repo.FeishuTokens{}, &feishuOAuthError{code: out.Code}
	}
	if status != http.StatusOK || out.AccessToken == "" {
		return repo.FeishuTokens{}, fmt.Errorf("feishu oauth token failed: HTTP %d", status)
	}
	now := time.Now()
	tokens := repo.FeishuTokens{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, Scopes: strings.Fields(out.Scope)}
	if out.ExpiresIn > 0 {
		tokens.AccessExpiresAt = now.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	if out.RefreshExpiresIn > 0 {
		tokens.RefreshExpiresAt = now.Add(time.Duration(out.RefreshExpiresIn) * time.Second)
	}
	return tokens, nil
}

func (c *feishuClient) exchangeConnectCode(ctx context.Context, code, verifier, redirectURI string) (repo.FeishuTokens, error) {
	tokens, err := c.oauthToken(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI},
	})
	var oauthErr *feishuOAuthError
	if errors.As(err, &oauthErr) && feishuCodeRejectedCodes[oauthErr.code] {
		return tokens, &feishuAuthCodeError{code: oauthErr.code}
	}
	return tokens, err
}

// refreshUserToken is the repo.FeishuRefreshFunc: it marks a refresh token
// Feishu refuses for good with repo.ErrFeishuGrantRejected.
func (c *feishuClient) refreshUserToken(ctx context.Context, refreshToken string) (repo.FeishuTokens, error) {
	tokens, err := c.oauthToken(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
	var oauthErr *feishuOAuthError
	if errors.As(err, &oauthErr) && feishuRefreshRejectedCodes[oauthErr.code] {
		return tokens, fmt.Errorf("%w (code %d)", repo.ErrFeishuGrantRejected, oauthErr.code)
	}
	return tokens, err
}

// revoke invalidates a refresh token at Feishu (RFC 7009); Feishu answers
// 200 even for unknown tokens.
func (c *feishuClient) revoke(ctx context.Context, refreshToken string) error {
	var out struct {
		Code int `json:"code"`
	}
	status, err := c.postForm(ctx, c.accountsURL+"/oauth/v1/revoke", url.Values{
		"client_id": {c.appID}, "client_secret": {c.appSecret}, "token": {refreshToken}, "token_type_hint": {"refresh_token"},
	}, &out)
	if err != nil {
		return err
	}
	if status != http.StatusOK || out.Code != 0 {
		return fmt.Errorf("feishu revoke failed: HTTP %d code %d", status, out.Code)
	}
	return nil
}

// postJSON sends a JSON POST and decodes a JSON body of any status (the
// token endpoint answers errors with HTTP 400 and a code).
func (c *feishuClient) postJSON(ctx context.Context, endpoint string, body any, out any) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(data)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return c.send(req, out)
}

// postForm sends a form POST and decodes a JSON body of any status (the v3
// endpoints answer errors with HTTP 400 and a code). An empty body decodes
// to nothing.
func (c *feishuClient) postForm(ctx context.Context, endpoint string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.send(req, out)
}

func (c *feishuClient) send(req *http.Request, out any) (int, error) {
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("feishu request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("feishu response read failed: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return resp.StatusCode, fmt.Errorf("feishu returned HTTP %d with an undecodable body", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// ---- tenant access token (bot identity), cached per app ----

type feishuTenantTokenEntry struct {
	token     string
	expiresAt time.Time
}

var feishuTenantTokens = struct {
	sync.Mutex
	entries map[string]feishuTenantTokenEntry
}{entries: map[string]feishuTenantTokenEntry{}}

// tenantAccessToken returns the app's tenant_access_token, reusing the
// process cache until feishuTenantTokenLead before it expires.
// ponytail: one process-wide lock held across the fetch, so concurrent rounds
// wait for a single request; per-app locks if several apps ever share a process.
func (c *feishuClient) tenantAccessToken(ctx context.Context) (string, error) {
	key := c.baseURL + "|" + c.appID
	feishuTenantTokens.Lock()
	defer feishuTenantTokens.Unlock()
	if cached, ok := feishuTenantTokens.entries[key]; ok && time.Now().Add(feishuTenantTokenLead).Before(cached.expiresAt) {
		return cached.token, nil
	}
	body, err := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	var out struct {
		Code              int    `json:"code"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	if out.Code != 0 || out.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu tenant_access_token failed: code %d", out.Code)
	}
	feishuTenantTokens.entries[key] = feishuTenantTokenEntry{token: out.TenantAccessToken, expiresAt: time.Now().Add(time.Duration(out.Expire) * time.Second)}
	return out.TenantAccessToken, nil
}

// feishuUserAccess returns the member's user access token for one round,
// refreshing it when less than feishuRoundMinValid is left.
// repo.ErrFeishuNotConnected means the round gets the bot identity only.
func (d *deps) feishuUserAccess(ctx context.Context, cfg feishuConfig, userID int64) (repo.FeishuAccess, error) {
	client := newFeishuClient(cfg)
	return d.repo.FeishuUserToken(ctx, userID, feishuRoundMinValid, client.refreshUserToken)
}

// ---- PKCE (RFC 7636, S256) ----

func newPKCEVerifier() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ---- handlers (usersHandlers: a grant is a personal credential) ----

type feishuGrantStatus struct {
	Enabled          bool                `json:"enabled"`
	Connected        bool                `json:"connected"`
	Status           string              `json:"status,omitempty"`
	Name             string              `json:"name,omitempty"`
	Tenant           string              `json:"tenant,omitempty"`
	Preset           string              `json:"preset,omitempty"`
	Scopes           []string            `json:"scopes,omitempty"`
	AccessExpiresAt  *time.Time          `json:"accessExpiresAt,omitempty"`
	RefreshExpiresAt *time.Time          `json:"refreshExpiresAt,omitempty"`
	UpdatedAt        *time.Time          `json:"updatedAt,omitempty"`
	Presets          map[string][]string `json:"presets,omitempty"`
}

func (h *usersHandlers) feishuGrantStatus(userID int64) feishuGrantStatus {
	out := feishuGrantStatus{Enabled: feishuConfigFromEnv().enabled()}
	if !out.Enabled {
		return out
	}
	out.Presets = map[string][]string{}
	for preset := range feishuScopePresets {
		out.Presets[preset] = feishuPresetScopes(preset)
	}
	grant, ok := h.repo.FeishuGrant(userID)
	if !ok {
		return out
	}
	out.Status, out.Name, out.Tenant, out.Preset, out.Scopes = grant.Status, grant.Name, grant.Tenant, grant.Preset, grant.Scopes
	out.Connected = grant.Status == repo.FeishuGrantActive
	out.AccessExpiresAt, out.RefreshExpiresAt, out.UpdatedAt = nonZeroTime(grant.AccessExpiresAt), nonZeroTime(grant.RefreshExpiresAt), nonZeroTime(grant.UpdatedAt)
	return out
}

func nonZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// feishuStatus is GET /api/users/me/feishu. It never returns a token.
func (h *usersHandlers) feishuStatus(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user == nil || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录"})
		return
	}
	c.JSON(http.StatusOK, h.feishuGrantStatus(user.ID))
}

// feishuDisconnect is DELETE /api/users/me/feishu: the tokens are deleted
// at once (the next round gets the bot identity only), then the refresh
// token is revoked at Feishu on a best-effort basis.
func (h *usersHandlers) feishuDisconnect(c *gin.Context) {
	user, ok := h.requestUser(c.Request, false)
	if !ok || user == nil || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录"})
		return
	}
	refresh, err := h.repo.RevokeFeishuGrant(user.ID)
	if err != nil {
		respond(c, err)
		return
	}
	if cfg := feishuConfigFromEnv(); refresh != "" && cfg.enabled() {
		ctx, cancel := context.WithTimeout(c.Request.Context(), feishuHTTPTimeout)
		defer cancel()
		if err := newFeishuClient(cfg).revoke(ctx, refresh); err != nil {
			log.Printf("[feishu] revoke grant for user %d failed (tokens already deleted): %v", user.ID, err)
		}
	}
	c.JSON(http.StatusOK, h.feishuGrantStatus(user.ID))
}

// feishuConnectStart is GET /api/users/me/feishu/connect?preset=readonly|
// readwrite, opened by the browser. It redirects to Feishu's authorization
// page with PKCE and a one-time state bound to the signed-in member.
func (h *usersHandlers) feishuConnectStart(c *gin.Context) {
	cfg := feishuConfigFromEnv()
	if !cfg.enabled() {
		feishuConnectPage(c, http.StatusServiceUnavailable, "飞书未启用", "服务没有配置飞书应用的 App ID 和 App Secret。")
		return
	}
	user, ok := h.requestUser(c.Request, false)
	if !ok || user == nil || user.IsGuest || user.ID <= 0 {
		feishuConnectPage(c, http.StatusUnauthorized, "请先登录", "登录平台后再连接飞书。")
		return
	}
	preset := strings.TrimSpace(c.Query("preset"))
	if preset == "" {
		preset = feishuDefaultPreset
	}
	if _, known := feishuScopePresets[preset]; !known {
		feishuConnectPage(c, http.StatusBadRequest, "未知的权限档位", "权限档位只能是 readonly（只读）或 readwrite（读写）。")
		return
	}
	verifier, err := newPKCEVerifier()
	if err != nil {
		feishuConnectPage(c, http.StatusInternalServerError, "无法开始授权", "生成校验值失败，请重试。")
		return
	}
	state, err := h.feishuStateStoreOrInit().issue(feishuStateEntry{purpose: feishuPurposeConnect, userID: user.ID, preset: preset, verifier: verifier}, feishuStateTTL)
	if err != nil {
		feishuConnectPage(c, http.StatusInternalServerError, "无法开始授权", "生成校验值失败，请重试。")
		return
	}
	setFeishuStateCookie(c, state)
	c.Redirect(http.StatusFound, cfg.AccountsURL+"/open-apis/authen/v1/authorize?"+url.Values{
		"client_id":             {cfg.AppID},
		"redirect_uri":          {cfg.redirectURI(c.Request)},
		"response_type":         {"code"},
		"state":                 {state},
		"scope":                 {strings.Join(feishuPresetScopes(preset), " ")},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"prompt":                {"consent"},
	}.Encode())
}

// feishuConnectCallback finishes a connection once feishuCallback has
// checked and consumed the state.
func (h *usersHandlers) feishuConnectCallback(c *gin.Context, cfg feishuConfig, entry feishuStateEntry) {
	current, ok := h.requestUser(c.Request, false)
	if !ok || current == nil || current.ID != entry.userID {
		feishuConnectPage(c, http.StatusForbidden, "账号不一致", "当前登录的账号不是发起飞书授权的账号，请重新登录后再连接。")
		return
	}
	if reason := strings.TrimSpace(c.Query("error")); reason != "" {
		feishuConnectPage(c, http.StatusBadRequest, "飞书没有完成授权", html.EscapeString(reason))
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		feishuConnectPage(c, http.StatusBadRequest, "飞书没有返回授权码", "请回到个人配置重新连接飞书。")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*feishuHTTPTimeout)
	defer cancel()
	client := newFeishuClient(cfg)
	tokens, err := client.exchangeConnectCode(ctx, code, entry.verifier, cfg.redirectURI(c.Request))
	if err != nil {
		var codeErr *feishuAuthCodeError
		if errors.As(err, &codeErr) {
			log.Printf("[feishu] connect exchange for user %d rejected the code: %v", entry.userID, err)
			feishuConnectPage(c, http.StatusBadRequest, "授权码无效或已过期", "请回到个人配置重新连接飞书。")
			return
		}
		log.Printf("[feishu] connect exchange for user %d failed: %v", entry.userID, err)
		feishuConnectPage(c, http.StatusBadGateway, "飞书暂时不可用", "请稍后再试。")
		return
	}
	if tokens.RefreshToken == "" {
		feishuConnectPage(c, http.StatusBadGateway, "飞书没有返回长期授权", "飞书应用需要开通 offline_access 权限，请联系平台管理员。")
		return
	}
	info, err := client.userInfo(ctx, tokens.AccessToken)
	if err != nil {
		log.Printf("[feishu] connect user_info for user %d failed: %v", entry.userID, err)
		feishuConnectPage(c, http.StatusBadGateway, "飞书暂时不可用", "没有拿到飞书用户信息，请稍后再试。")
		return
	}
	if !cfg.tenantAllowed(info.TenantKey) {
		log.Printf("[feishu] connect denied: tenant_key %q not in LOOPFORGE_FEISHU_ALLOWED_TENANTS", info.TenantKey)
		feishuConnectPage(c, http.StatusForbidden, "该飞书企业未被允许",
			"请把下面的 tenant_key 交给平台管理员，加入白名单 LOOPFORGE_FEISHU_ALLOWED_TENANTS：<br><code>"+html.EscapeString(info.TenantKey)+"</code>")
		return
	}
	subject := info.UnionID
	if subject == "" {
		subject = info.OpenID
	}
	if subject == "" {
		feishuConnectPage(c, http.StatusBadGateway, "飞书暂时不可用", "没有拿到飞书用户标识，请稍后再试。")
		return
	}
	// An account that signs in with Feishu may only connect that identity.
	if own := h.repo.IdentitiesOf(entry.userID, repo.IdentityProviderFeishu); len(own) > 0 {
		match := false
		for _, id := range own {
			match = match || (id.Tenant == info.TenantKey && id.Subject == subject)
		}
		if !match {
			feishuConnectPage(c, http.StatusForbidden, "飞书账号不一致", "请用登录本平台的飞书账号授权。")
			return
		}
	}
	// An identity another account signs in with cannot be shared.
	if other, bound := h.repo.UserByIdentity(repo.IdentityProviderFeishu, info.TenantKey, subject); bound && other.ID != entry.userID {
		feishuConnectPage(c, http.StatusForbidden, "该飞书账号已被占用", "这个飞书账号是平台上另一个账号的登录身份，不能再连接到当前账号。")
		return
	}
	if err := h.repo.SaveFeishuGrant(entry.userID, repo.FeishuGrantInput{
		Tenant: info.TenantKey, Subject: subject, OpenID: info.OpenID, Name: info.Name, Preset: entry.preset, Tokens: tokens,
	}); err != nil {
		log.Printf("[feishu] save grant for user %d failed: %v", entry.userID, err)
		feishuConnectPage(c, http.StatusInternalServerError, "保存授权失败", "请稍后重试。")
		return
	}
	c.Redirect(http.StatusFound, "/?feishu=connected#/settings")
}

// feishuConnectPage renders a small HTML page linking back to personal
// settings. title is escaped; message may already contain safe HTML.
func feishuConnectPage(c *gin.Context, status int, title, message string) {
	c.Data(status, "text/html; charset=utf-8", []byte(
		"<!doctype html><meta charset=utf-8><title>"+html.EscapeString(title)+
			"</title><h1>"+html.EscapeString(title)+"</h1><p>"+message+
			"</p><p><a href='/#/settings'>返回个人配置</a></p>"))
}
