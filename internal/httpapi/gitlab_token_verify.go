package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// GitLab 的个人访问令牌需要 api 权限：开 MR、合并都要调用写接口，
// 只有 read_repository / write_repository 不够。校验调用
// /api/v4/user 确认身份，再调用 /api/v4/personal_access_tokens/self 读取
// scopes 检查是否包含 api。老版本 GitLab 若没有 self 端点（404），退回
// 仅凭 /user 成功判定有效（无法确认 scope，前端据 Missing 为空提示）。
var requiredGitLabScopes = []string{"api"}

// verifyGitLabToken 校验当前登录用户的 GitLab 个人访问令牌。
// GET 校验已保存的令牌；POST 可在 body.token 里带上尚未保存的令牌。
// 令牌只发往配置的 GitLab 实例，不落库。始终返回 200 + 结构化结果。
func (h *usersHandlers) verifyGitLabToken(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gitTokenVerifyResult{Error: "valid user id is required"})
		return
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID != id {
		c.JSON(http.StatusForbidden, gitTokenVerifyResult{Error: "只能校验当前登录用户的凭据"})
		return
	}
	base := domain.GitLabAPIBase()
	if base == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "GitLab 未启用：请先配置 LOOPFORGE_GITLAB_URL"})
		return
	}
	token := ""
	if c.Request.Method == http.MethodPost {
		var req struct {
			Token string `json:"token"`
		}
		if !decode(c, &req) {
			return
		}
		token = strings.TrimSpace(req.Token)
	}
	if token == "" {
		if h.repo == nil {
			c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "存储不可用"})
			return
		}
		user, ok := h.repo.User(id)
		if !ok {
			c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "用户不存在"})
			return
		}
		token = gitlabRawToken(user.GitLabToken)
	}
	token = gitlabRawToken(token)
	if token == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "尚未配置 GitLab Token，请先在个人配置中填写"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	c.JSON(http.StatusOK, verifyGitLabToken(ctx, base, token))
}

// gitlabRawToken strips an "oauth2:" git-credential prefix so the API call
// sends the bare personal access token.
func gitlabRawToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, ':'); i >= 0 && strings.EqualFold(raw[:i], "oauth2") {
		return strings.TrimSpace(raw[i+1:])
	}
	return raw
}

func verifyGitLabToken(ctx context.Context, base, token string) gitTokenVerifyResult {
	login, err := gitlabTokenUser(ctx, base, token)
	if err != nil {
		return gitTokenVerifyResult{Valid: false, Error: err.Error()}
	}
	scopes, selfErr := gitlabTokenScopes(ctx, base, token)
	if selfErr != nil {
		// Identity is proven; scope could not be read (older GitLab). Treat as
		// valid but without a scope guarantee.
		return gitTokenVerifyResult{Valid: true, Login: login}
	}
	missing := missingGitTokenScopes(scopes, requiredGitLabScopes)
	return gitTokenVerifyResult{Valid: len(missing) == 0, Login: login, Scopes: scopes, Missing: missing}
}

func gitlabTokenUser(ctx context.Context, base, token string) (string, error) {
	var user struct {
		Username string `json:"username"`
	}
	status, err := gitlabGet(ctx, base+"/user", token, &user)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitLab 实例：%v", err)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", errors.New("令牌无效或已过期：请确认填写的是 GitLab「个人访问令牌」并勾选 api 权限")
	}
	if status != http.StatusOK || user.Username == "" {
		return "", fmt.Errorf("GitLab 返回 HTTP %d", status)
	}
	return user.Username, nil
}

func gitlabTokenScopes(ctx context.Context, base, token string) ([]string, error) {
	var info struct {
		Scopes  []string `json:"scopes"`
		Active  bool     `json:"active"`
		Revoked bool     `json:"revoked"`
	}
	status, err := gitlabGet(ctx, base+"/personal_access_tokens/self", token, &info)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, errors.New("self endpoint unavailable")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GitLab 返回 HTTP %d", status)
	}
	if info.Revoked || !info.Active {
		return nil, errors.New("令牌已被撤销或未激活")
	}
	return info.Scopes, nil
}

// gitlabGet performs an authenticated GET and decodes a 2xx body into out.
// It returns the HTTP status so callers can distinguish 404/401 from success.
func gitlabGet(ctx context.Context, rawURL, token string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("GitLab 返回内容无法解析（HTTP %d）", resp.StatusCode)
		}
	}
	return resp.StatusCode, nil
}
