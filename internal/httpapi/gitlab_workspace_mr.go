package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// workspaceGitLabMR is a merge request the platform found or opened for a
// branch the speaker pushed from a conversation workspace.
type workspaceGitLabMR struct {
	Project      string
	IID          int
	URL          string
	Title        string
	Body         string
	TargetBranch string
}

// gitlabEncodeProjectPath URL-encodes a project path for use as the GitLab
// ":id" path segment: every "/" becomes "%2F" (url.PathEscape leaves "/"
// untouched), so sub-group paths address the right project.
func gitlabEncodeProjectPath(path string) string {
	return strings.ReplaceAll(url.PathEscape(path), "/", "%2F")
}

// ensureGitLabDeliveryMR opens (or finds) the GitLab merge request for a SOP
// run's work branch, authenticated as the run's responsible user. It is wired
// into the engine as Engine.EnsureDeliveryMR and reuses the same find-or-create
// as the conversation workspace path. A non-GitLab project, a disabled
// instance or a responsible user without a token is a no-op / reported error.
func (h *assistantHandlers) ensureGitLabDeliveryMR(projectID, sourceBranch, targetBranch string, responsibleUserID int64) (domain.DeliveryMR, bool, error) {
	if !domain.IsGitLabProject(projectID) || !domain.GitLabEnabled() {
		return domain.DeliveryMR{}, false, nil
	}
	base := domain.GitLabAPIBase()
	if base == "" || h.repo == nil || responsibleUserID <= 0 {
		return domain.DeliveryMR{}, false, nil
	}
	user, ok := h.repo.User(responsibleUserID)
	if !ok {
		return domain.DeliveryMR{}, false, nil
	}
	token := gitlabRawToken(user.GitLabToken)
	if token == "" {
		return domain.DeliveryMR{}, false, fmt.Errorf("运行责任人未配置 GitLab 令牌，无法创建合并请求")
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnDeliveryTimeout)
	defer cancel()
	mr, ok, err := ensureGitLabMR(ctx, base, token, projectID, sourceBranch, targetBranch)
	if err != nil || !ok {
		return domain.DeliveryMR{}, ok, err
	}
	return domain.DeliveryMR{Project: projectID, IID: mr.IID, URL: mr.URL, TargetBranch: mr.TargetBranch}, true, nil
}

// ensureGitLabMR finds, or opens, the merge request from source into target
// for one GitLab project, using token (the raw PAT). It is the single
// find-or-create used by both the conversation-workspace delivery and the SOP
// delivery path. When target is empty the project's default branch is used.
func ensureGitLabMR(ctx context.Context, base, token, project, source, target string) (workspaceGitLabMR, bool, error) {
	path := strings.TrimPrefix(project, "gitlab:")
	if path == "" || strings.TrimSpace(source) == "" {
		return workspaceGitLabMR{}, false, nil
	}
	escaped := gitlabEncodeProjectPath(path)
	if strings.TrimSpace(target) == "" {
		var meta struct {
			DefaultBranch string `json:"default_branch"`
		}
		if status, err := gitlabGet(ctx, base+"/projects/"+escaped, token, &meta); err != nil {
			return workspaceGitLabMR{}, false, err
		} else if status != http.StatusOK {
			return workspaceGitLabMR{}, false, fmt.Errorf("GitLab 返回 HTTP %d", status)
		}
		target = strings.TrimSpace(meta.DefaultBranch)
		if target == "" {
			target = "main"
		}
	}
	if domain.SameGitBranch(source, target) {
		return workspaceGitLabMR{}, false, nil
	}
	return gitlabFindOrCreateWorkspaceMR(ctx, base, token, escaped, project, source, target)
}

// workspaceGitLabMRs finds or opens a merge request for each pushed branch of
// one GitLab project, targeting the project's default branch. Branches that
// are not on the remote (never pushed) or equal the default branch are
// skipped. An existing non-closed MR is reused; otherwise one is created.
func workspaceGitLabMRs(ctx context.Context, base, token, project string, branches map[string]bool) ([]workspaceGitLabMR, error) {
	path := strings.TrimPrefix(project, "gitlab:")
	if path == "" {
		return nil, nil
	}
	escaped := gitlabEncodeProjectPath(path)
	var meta struct {
		DefaultBranch string `json:"default_branch"`
	}
	if status, err := gitlabGet(ctx, base+"/projects/"+escaped, token, &meta); err != nil {
		return nil, err
	} else if status != http.StatusOK {
		return nil, fmt.Errorf("GitLab 返回 HTTP %d", status)
	}
	def := strings.TrimSpace(meta.DefaultBranch)
	ordered := make([]string, 0, len(branches))
	for b := range branches {
		ordered = append(ordered, b)
	}
	sort.Strings(ordered)
	out := []workspaceGitLabMR{}
	for _, branch := range ordered {
		branch = strings.TrimSpace(branch)
		if branch == "" || (def != "" && domain.SameGitBranch(branch, def)) {
			continue
		}
		// Only branches that were actually pushed can carry an MR.
		status, err := gitlabGet(ctx, base+"/projects/"+escaped+"/repository/branches/"+gitlabEncodeProjectPath(branch), token, nil)
		if err != nil {
			log.Printf("gitlab branch lookup %s %s: %v", path, branch, err)
			continue
		}
		if status != http.StatusOK {
			continue
		}
		target := def
		if target == "" {
			target = "main"
		}
		mr, ok, err := gitlabFindOrCreateWorkspaceMR(ctx, base, token, escaped, project, branch, target)
		if err != nil {
			log.Printf("gitlab merge request for %s %s: %v", path, branch, err)
			continue
		}
		if ok {
			out = append(out, mr)
		}
	}
	return out, nil
}

func gitlabFindOrCreateWorkspaceMR(ctx context.Context, base, token, escaped, project, branch, target string) (workspaceGitLabMR, bool, error) {
	var list []struct {
		IID          int    `json:"iid"`
		State        string `json:"state"`
		Title        string `json:"title"`
		TargetBranch string `json:"target_branch"`
	}
	q := url.Values{"source_branch": {branch}, "state": {"all"}, "per_page": {"20"}, "order_by": {"updated_at"}, "sort": {"desc"}}
	if status, err := gitlabGet(ctx, base+"/projects/"+escaped+"/merge_requests?"+q.Encode(), token, &list); err != nil {
		return workspaceGitLabMR{}, false, err
	} else if status != http.StatusOK {
		return workspaceGitLabMR{}, false, fmt.Errorf("GitLab 返回 HTTP %d", status)
	}
	for _, m := range list {
		if m.IID <= 0 || strings.EqualFold(m.State, "closed") {
			continue
		}
		tb := strings.TrimSpace(m.TargetBranch)
		if tb == "" {
			tb = target
		}
		return workspaceGitLabMR{Project: project, IID: m.IID, URL: domain.GitLabMRURL(project, m.IID), Title: strings.TrimSpace(m.Title), TargetBranch: tb}, true, nil
	}
	var created struct {
		IID          int    `json:"iid"`
		TargetBranch string `json:"target_branch"`
	}
	body := map[string]any{"source_branch": branch, "target_branch": target, "title": branch}
	status, err := gitlabPost(ctx, base+"/projects/"+escaped+"/merge_requests", token, body, &created)
	if err != nil {
		return workspaceGitLabMR{}, false, err
	}
	if status < 200 || status >= 300 || created.IID <= 0 {
		return workspaceGitLabMR{}, false, fmt.Errorf("GitLab 创建 MR 返回 HTTP %d", status)
	}
	tb := strings.TrimSpace(created.TargetBranch)
	if tb == "" {
		tb = target
	}
	return workspaceGitLabMR{Project: project, IID: created.IID, URL: domain.GitLabMRURL(project, created.IID), Title: branch, TargetBranch: tb}, true, nil
}

func gitlabPost(ctx context.Context, rawURL, token string, in, out any) (int, error) {
	payload, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}
