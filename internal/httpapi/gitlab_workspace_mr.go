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

// workspaceGitLabMRs finds or opens a merge request for each pushed branch of
// one GitLab project, targeting the project's default branch. Branches that
// are not on the remote (never pushed) or equal the default branch are
// skipped. An existing non-closed MR is reused; otherwise one is created.
func workspaceGitLabMRs(ctx context.Context, base, token, project string, branches map[string]bool) ([]workspaceGitLabMR, error) {
	path := strings.TrimPrefix(project, "gitlab:")
	if path == "" {
		return nil, nil
	}
	escaped := url.PathEscape(path)
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
		status, err := gitlabGet(ctx, base+"/projects/"+escaped+"/repository/branches/"+url.PathEscape(branch), token, nil)
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
