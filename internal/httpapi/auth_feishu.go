package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// Feishu (飞书, open.feishu.cn) web login. It preserves password sign-in and
// auto-provisions a new user for a Feishu member the first time they sign in,
// recognising them only by (union_id or open_id, tenant_key) — never by login
// name — so it can never take over an existing password account.
//
// The flow mirrors the reference adapter's protocol: /start redirects to
// Feishu's authorization page carrying a one-time state (also set in an
// HttpOnly cookie); /callback checks the state against the cookie and the
// server-side single-use store, exchanges the code for the member's identity
// through three Feishu API calls, enforces the tenant whitelist, then binds or
// provisions the account and signs the browser in with the existing signed
// session cookie.

const (
	feishuStateCookieName = "agentflow_feishu_state"
	feishuStateCookiePath = "/api/auth/feishu"
	feishuCallbackPath    = "/api/auth/feishu/callback"
	feishuStateTTL        = 10 * time.Minute
	feishuHTTPTimeout     = 10 * time.Second
	defaultFeishuBaseURL  = "https://open.feishu.cn"
	// defaultFeishuAccountsURL serves the OAuth 2.0 endpoints.
	defaultFeishuAccountsURL = "https://accounts.feishu.cn"
	// Feishu's authorization page ("Obtain the OAuth code"), China domain.
	feishuAuthorizeURL = "https://accounts.feishu.cn/open-apis/authen/v1/authorize"
	stateRandomBytes   = 32
)

// Feishu oidc access_token error codes that reject the code itself: 20003
// "invalid, could only be used once", 20004 "expired".
const (
	feishuCodeInvalid = 20003
	feishuCodeExpired = 20004
)

// ---- configuration ----

type feishuConfig struct {
	AppID          string
	AppSecret      string
	RedirectURI    string
	AllowedTenants map[string]bool
	BaseURL        string
	// AccountsURL hosts the OAuth endpoints (authorize, v3 token, revoke).
	// It follows LOOPFORGE_FEISHU_BASE_URL when that is overridden, so a
	// stand-in Feishu (tests, a private gateway) serves both.
	AccountsURL string
}

func feishuConfigFromEnv() feishuConfig {
	tenants := map[string]bool{}
	for _, tenant := range strings.FieldsFunc(os.Getenv("LOOPFORGE_FEISHU_ALLOWED_TENANTS"), func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '\n' || r == '\t'
	}) {
		if tenant = strings.TrimSpace(tenant); tenant != "" {
			tenants[tenant] = true
		}
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("LOOPFORGE_FEISHU_BASE_URL")), "/")
	accounts := base
	if base == "" {
		base, accounts = defaultFeishuBaseURL, defaultFeishuAccountsURL
	}
	return feishuConfig{
		AppID:          strings.TrimSpace(os.Getenv("LOOPFORGE_FEISHU_APP_ID")),
		AppSecret:      strings.TrimSpace(os.Getenv("LOOPFORGE_FEISHU_APP_SECRET")),
		RedirectURI:    strings.TrimSpace(os.Getenv("LOOPFORGE_FEISHU_REDIRECT_URI")),
		AllowedTenants: tenants,
		BaseURL:        base,
		AccountsURL:    accounts,
	}
}

// enabled reports whether Feishu sign-in is offered on the login page. The app
// id and secret must be set; the redirect URI is optional and derived from the
// request when empty (redirectURI). The tenant whitelist is enforced at the
// callback, not here: an enabled but empty whitelist shows the button and
// denies every sign-in with the tenant_key to allowlist.
func (c feishuConfig) enabled() bool {
	return c.AppID != "" && c.AppSecret != ""
}

func (c feishuConfig) tenantAllowed(tenantKey string) bool {
	return c.AllowedTenants[strings.TrimSpace(tenantKey)]
}

// redirectURI is the configured callback, or one derived from the request's
// own scheme and host. Feishu validates it against the app's registered
// redirect URLs, so a derived value cannot send the browser off-site.
func (c feishuConfig) redirectURI(r *http.Request) string {
	if c.RedirectURI != "" {
		return c.RedirectURI
	}
	proto := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		proto = "https"
	}
	return fmt.Sprintf("%s://%s%s", proto, r.Host, feishuCallbackPath)
}

// feishuSignInEnabled is the login page's capability probe (authSetup).
func feishuSignInEnabled() bool { return feishuConfigFromEnv().enabled() }

// isAbsoluteHTTPURL reports whether raw is an absolute http(s) URL, used to
// keep a Feishu avatar from being overwritten with the local photo path.
func isAbsoluteHTTPURL(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

// ---- one-time OAuth state ----

// feishuStateEntry is one in-flight authorization. purpose tells the shared
// callback whether it is a sign-in or a member connecting their Feishu grant
// (feishu_connect.go), which also carries the member, the chosen scope preset
// and the PKCE verifier.
type feishuStateEntry struct {
	expiresAt time.Time
	returnURL string
	purpose   string
	userID    int64
	preset    string
	verifier  string
	// bind also makes the proven identity a sign-in of the account
	// (purpose connect with 「同时用于登录」, or purpose bind).
	bind bool
}

const (
	feishuPurposeLogin   = ""
	feishuPurposeConnect = "connect"
	feishuPurposeBind    = "bind"
)

// feishuStateStore remembers the in-flight OAuth states of one server. A state
// is single use: consume deletes it, so a replayed callback is rejected even
// within the TTL.
type feishuStateStore struct {
	mu      sync.Mutex
	entries map[string]feishuStateEntry
	now     func() time.Time
}

func newFeishuStateStore() *feishuStateStore {
	return &feishuStateStore{entries: map[string]feishuStateEntry{}, now: time.Now}
}

func (s *feishuStateStore) issue(entry feishuStateEntry, ttl time.Duration) (string, error) {
	buf := make([]byte, stateRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry.expiresAt = s.now().Add(ttl)
	s.entries[state] = entry
	return state, nil
}

// consume removes and returns the entry; a missing, expired or already used
// state returns ok=false.
func (s *feishuStateStore) consume(state string) (feishuStateEntry, bool) {
	if strings.TrimSpace(state) == "" {
		return feishuStateEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry, ok := s.entries[state]
	if !ok {
		return feishuStateEntry{}, false
	}
	delete(s.entries, state)
	if s.now().After(entry.expiresAt) {
		return feishuStateEntry{}, false
	}
	return entry, true
}

func (s *feishuStateStore) pruneLocked() {
	now := s.now()
	for key, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, key)
		}
	}
}

// feishuStateStoreInit guards lazy creation for handlers built without New.
var feishuStateStoreInit sync.Mutex

func (d *deps) feishuStateStoreOrInit() *feishuStateStore {
	feishuStateStoreInit.Lock()
	if d.feishuStates == nil {
		d.feishuStates = newFeishuStateStore()
	}
	store := d.feishuStates
	feishuStateStoreInit.Unlock()
	return store
}

// ---- Feishu API client (net/http, no SDK) ----

type feishuUserInfo struct {
	OpenID    string
	UnionID   string
	Name      string
	AvatarURL string
	TenantKey string
}

// feishuAuthCodeError marks a code Feishu rejected as invalid or expired, so
// the callback can tell the member to retry instead of reporting an outage.
type feishuAuthCodeError struct{ code int }

func (e *feishuAuthCodeError) Error() string {
	return fmt.Sprintf("feishu rejected the authorization code (code %d)", e.code)
}

type feishuClient struct {
	baseURL     string
	accountsURL string
	appID       string
	appSecret   string
	http        *http.Client
}

func newFeishuClient(cfg feishuConfig) *feishuClient {
	return &feishuClient{
		baseURL:     cfg.BaseURL,
		accountsURL: cfg.AccountsURL,
		appID:       cfg.AppID,
		appSecret:   cfg.AppSecret,
		http:        &http.Client{Timeout: feishuHTTPTimeout},
	}
}

// exchange runs the three sign-in calls: the app access token, the member's
// user access token from the code, then the member's profile. Only the login
// scope is used; no directory (contact) call is made.
func (c *feishuClient) exchange(ctx context.Context, code string) (feishuUserInfo, error) {
	appToken, err := c.appAccessToken(ctx)
	if err != nil {
		return feishuUserInfo{}, err
	}
	userToken, err := c.userAccessToken(ctx, appToken, code)
	if err != nil {
		return feishuUserInfo{}, err
	}
	return c.userInfo(ctx, userToken)
}

func (c *feishuClient) appAccessToken(ctx context.Context) (string, error) {
	body, err := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/open-apis/auth/v3/app_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	var out struct {
		Code           int    `json:"code"`
		Msg            string `json:"msg"`
		AppAccessToken string `json:"app_access_token"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	if out.Code != 0 || out.AppAccessToken == "" {
		return "", fmt.Errorf("feishu app_access_token failed: code %d", out.Code)
	}
	return out.AppAccessToken, nil
}

func (c *feishuClient) userAccessToken(ctx context.Context, appToken, code string) (string, error) {
	body, err := json.Marshal(map[string]string{"grant_type": "authorization_code", "code": code})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/open-apis/authen/v1/oidc/access_token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+appToken)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	if out.Code == feishuCodeInvalid || out.Code == feishuCodeExpired {
		return "", &feishuAuthCodeError{code: out.Code}
	}
	if out.Code != 0 || out.Data.AccessToken == "" {
		return "", fmt.Errorf("feishu oidc access_token failed: code %d", out.Code)
	}
	return out.Data.AccessToken, nil
}

func (c *feishuClient) userInfo(ctx context.Context, userToken string) (feishuUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/open-apis/authen/v1/user_info", nil)
	if err != nil {
		return feishuUserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			OpenID    string `json:"open_id"`
			UnionID   string `json:"union_id"`
			Name      string `json:"name"`
			AvatarURL string `json:"avatar_url"`
			TenantKey string `json:"tenant_key"`
		} `json:"data"`
	}
	if err := c.do(req, &out); err != nil {
		return feishuUserInfo{}, err
	}
	if out.Code != 0 {
		return feishuUserInfo{}, fmt.Errorf("feishu user_info failed: code %d", out.Code)
	}
	return feishuUserInfo{
		OpenID:    strings.TrimSpace(out.Data.OpenID),
		UnionID:   strings.TrimSpace(out.Data.UnionID),
		Name:      strings.TrimSpace(out.Data.Name),
		AvatarURL: strings.TrimSpace(out.Data.AvatarURL),
		TenantKey: strings.TrimSpace(out.Data.TenantKey),
	}, nil
}

// do sends req and decodes a successful JSON body into out. Errors never
// include the request body or the response payload, so the app secret, the
// authorization code and any token stay out of logs.
func (c *feishuClient) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("feishu request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("feishu response read failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("feishu response could not be decoded")
	}
	return nil
}

// ---- handlers ----

// feishuReturnURL is where the callback sends the browser after sign-in. It
// reuses safeReturnURL to validate an explicit return_url against this site
// and otherwise defaults to the site root, so a sign-in started from the login
// page does not land back on it. Referer is deliberately not consulted.
func (h *authHandlers) feishuReturnURL(r *http.Request) string {
	proto := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		proto = "https"
	}
	origin := fmt.Sprintf("%s://%s", proto, r.Host)
	if safe, ok := h.safeReturnURL(r, origin, r.URL.Query().Get("return_url")); ok {
		return safe
	}
	return origin + "/"
}

func (h *authHandlers) feishuStart(c *gin.Context) {
	cfg := feishuConfigFromEnv()
	if !cfg.enabled() {
		feishuErrorPage(c, http.StatusServiceUnavailable, "飞书登录未启用", "服务没有配置飞书应用的 App ID 和 App Secret。")
		return
	}
	returnURL := h.feishuReturnURL(c.Request)
	state, err := h.feishuStateStoreOrInit().issue(feishuStateEntry{returnURL: returnURL}, feishuStateTTL)
	if err != nil {
		feishuErrorPage(c, http.StatusInternalServerError, "无法开始飞书登录", "生成校验值失败，请重试。")
		return
	}
	setFeishuStateCookie(c, state)
	authorize := feishuAuthorizeURL + "?" + url.Values{
		"client_id":     {cfg.AppID},
		"redirect_uri":  {cfg.redirectURI(c.Request)},
		"response_type": {"code"},
		"state":         {state},
	}.Encode()
	c.Redirect(http.StatusFound, authorize)
}

func (h *authHandlers) feishuCallback(c *gin.Context) {
	cfg := feishuConfigFromEnv()
	if !cfg.enabled() {
		feishuErrorPage(c, http.StatusServiceUnavailable, "飞书登录未启用", "服务没有配置飞书应用。")
		return
	}
	state := strings.TrimSpace(c.Query("state"))
	cookie, _ := c.Request.Cookie(feishuStateCookieName)
	clearFeishuStateCookie(c)
	// The state must be present in both the query and the HttpOnly cookie and
	// the two must be equal, before the server-side single-use check runs.
	if state == "" || cookie == nil || subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		feishuErrorPage(c, http.StatusBadRequest, "登录校验失败", "登录状态不一致，请回到登录页重新发起飞书登录。")
		return
	}
	entry, ok := h.feishuStateStoreOrInit().consume(state)
	if !ok {
		feishuErrorPage(c, http.StatusBadRequest, "登录已失效", "登录链接已过期或被使用过，请回到登录页重新发起飞书登录。")
		return
	}
	if entry.purpose != feishuPurposeLogin {
		h.users.feishuConnectCallback(c, cfg, entry)
		return
	}
	if reason := strings.TrimSpace(c.Query("error")); reason != "" {
		feishuErrorPage(c, http.StatusBadRequest, "飞书没有完成授权", html.EscapeString(reason))
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		feishuErrorPage(c, http.StatusBadRequest, "飞书没有返回授权码", "请回到登录页重新发起飞书登录。")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), feishuHTTPTimeout)
	defer cancel()
	info, err := newFeishuClient(cfg).exchange(ctx, code)
	if err != nil {
		var codeErr *feishuAuthCodeError
		if errors.As(err, &codeErr) {
			feishuErrorPage(c, http.StatusBadRequest, "授权码无效或已过期", "请回到登录页重新发起飞书登录。")
			return
		}
		log.Printf("[feishu] sign-in exchange failed: %v", err)
		feishuErrorPage(c, http.StatusBadGateway, "飞书登录暂时不可用", "请稍后再试。")
		return
	}
	// Tenant whitelist: an empty whitelist denies everyone. The rejected
	// tenant_key is logged and shown so an admin can allowlist it.
	if !cfg.tenantAllowed(info.TenantKey) {
		log.Printf("[feishu] sign-in denied: tenant_key %q not in LOOPFORGE_FEISHU_ALLOWED_TENANTS", info.TenantKey)
		feishuErrorPage(c, http.StatusForbidden, "该飞书企业未被允许登录",
			"请把下面的 tenant_key 交给平台管理员，加入白名单 LOOPFORGE_FEISHU_ALLOWED_TENANTS：<br><code>"+html.EscapeString(info.TenantKey)+"</code>")
		return
	}
	// The member is recognised by union_id, or open_id when union id is absent.
	subject := info.UnionID
	if subject == "" {
		subject = info.OpenID
	}
	if subject == "" {
		log.Printf("[feishu] sign-in failed: user_info has no union_id or open_id")
		feishuErrorPage(c, http.StatusBadGateway, "飞书登录暂时不可用", "没有拿到用户标识，请稍后再试。")
		return
	}
	user, err := h.resolveFeishuUser(subject, info)
	if err != nil {
		if errors.Is(err, errFeishuSignupDisabled) {
			feishuErrorPage(c, http.StatusForbidden, "自助注册已关闭", "平台未开启自助注册，请联系管理员先创建账号后再用飞书登录。")
			return
		}
		log.Printf("[feishu] provisioning failed: %v", err)
		feishuErrorPage(c, http.StatusInternalServerError, "创建账号失败", "请稍后再试。")
		return
	}
	authUser := domainUserToAuth(user)
	authUser.AuthTime = time.Now().UTC()
	if info.AvatarURL != "" {
		authUser.AvatarURL = info.AvatarURL
	} else {
		authUser.AvatarURL = auth.AvatarURL(user.LoginName, 100)
	}
	token, err := h.getAuthClient().SignUser(authUser)
	if err != nil {
		feishuErrorPage(c, http.StatusInternalServerError, "签发会话失败", "请稍后再试。")
		return
	}
	h.setAuthCookie(c.Writer, c.Request, token)
	c.Redirect(http.StatusFound, entry.returnURL)
}

var errFeishuSignupDisabled = errors.New("feishu self-service signup is disabled")

// resolveFeishuUser binds the sign-in to an existing Feishu account or, when
// permitted, provisions a new one. It never matches a password account: lookup
// and creation go only through the (feishu, tenant_key, subject) identity.
func (h *authHandlers) resolveFeishuUser(subject string, info feishuUserInfo) (domain.User, error) {
	identity := repo.ExternalIdentity{
		Provider:  repo.IdentityProviderFeishu,
		Tenant:    info.TenantKey,
		Subject:   subject,
		Name:      info.Name,
		AvatarURL: info.AvatarURL,
	}
	if existing, ok := h.repo.UserByIdentity(identity.Provider, identity.Tenant, identity.Subject); ok {
		if err := h.repo.RecordIdentitySignIn(identity); err != nil {
			log.Printf("[feishu] refresh profile for user %d failed: %v", existing.ID, err)
		} else if refreshed, ok := h.repo.User(existing.ID); ok {
			existing = refreshed
		}
		return existing, nil
	}
	// Serialize provisioning with password registration so the first-account
	// admin decision and login-name allocation stay race-free.
	registerMu.Lock()
	defer registerMu.Unlock()
	if existing, ok := h.repo.UserByIdentity(identity.Provider, identity.Tenant, identity.Subject); ok {
		return existing, nil
	}
	first := h.repo.UserCount() == 0
	if !first && !signupEnabled() {
		return domain.User{}, errFeishuSignupDisabled
	}
	created, err := h.repo.CreateIdentityAccount(identity, first)
	if err != nil {
		return domain.User{}, err
	}
	if !first {
		h.joinWelcomeProject(created.ID)
	}
	return created, nil
}

// ---- cookies & error page ----

func setFeishuStateCookie(c *gin.Context, state string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     feishuStateCookieName,
		Value:    state,
		Path:     feishuStateCookiePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(c.Request),
		MaxAge:   int(feishuStateTTL.Seconds()),
	})
}

func clearFeishuStateCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     feishuStateCookieName,
		Value:    "",
		Path:     feishuStateCookiePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(c.Request),
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

// feishuErrorPage renders a small HTML page with a link back to the login
// page. title is escaped; message may already contain safe HTML.
func feishuErrorPage(c *gin.Context, status int, title, message string) {
	c.Data(status, "text/html; charset=utf-8", []byte(
		"<!doctype html><meta charset=utf-8><title>"+html.EscapeString(title)+
			"</title><h1>"+html.EscapeString(title)+"</h1><p>"+message+
			"</p><p><a href='/login.html'>返回登录</a></p>"))
}
