package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitegress"
)

// GitLab delivery on a self-hosted instance reuses the upstream building
// blocks: the MR project key is "gitlab:<host>/<path>" (domain.ExternalMRProject),
// the token is the registering/owning user's per-host Git HTTP credential
// (GitHTTPCredentialStore), and every control-plane connection goes through
// gitegress, bounded by the administrator's HTTPS host authorization. Only the
// SOP-side find/create/merge is added here; conversation delivery, credentials,
// egress and status polling already exist upstream.

// gitHTTPSHostLookupStore is the subset of the store that resolves an HTTPS
// host to its administrator-approved addresses for gitegress.
type gitHTTPSHostLookupStore interface {
	PlatformGitHTTPSAddresses(context.Context, string) ([]netip.Addr, error)
}

func (s *Service) gitHTTPSLookup() gitegress.HTTPSHostLookup {
	if store, ok := s.projectAuth.(gitHTTPSHostLookupStore); ok {
		return store.PlatformGitHTTPSAddresses
	}
	return nil
}

// gitlabClient talks to one GitLab instance's v4 REST API with a per-host Git
// HTTP credential, over gitegress. The project path is URL-encoded as ":id".
type gitlabClient struct {
	base string // API root, e.g. https://code.example.net/api/v4
	path string // group/.../repo, not URL-encoded
	cred domain.GitHTTPCredentialSecret
	http *http.Client
}

// gitlabCredentialForProject resolves the owning user's credential for the
// project's host, mirroring githubClient's choice of the binding project Owner.
func (s *Service) gitlabCredentialForProject(project string) (domain.GitHTTPCredentialSecret, string, string, bool) {
	platform, host, path, ok := domain.ExternalMRProject(project)
	if !ok || platform != domain.GitPlatformGitLab {
		return domain.GitHTTPCredentialSecret{}, "", "", false
	}
	creds, ok := s.projectAuth.(GitHTTPCredentialStore)
	if !ok || s.projectAuth == nil {
		return domain.GitHTTPCredentialSecret{}, host, path, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, repo := range s.projectAuth.Repositories() {
		if repo.Provider != domain.GitPlatformGitLab || repo.GongfengProjectID != project {
			continue
		}
		if p, found := s.projectAuth.Project(repo.ProjectID); found {
			if cred, ok := creds.GitHTTPCredentialForHost(ctx, p.OwnerID, host); ok && cred.Platform == domain.GitPlatformGitLab {
				return cred, host, path, true
			}
		}
		break
	}
	return domain.GitHTTPCredentialSecret{}, host, path, false
}

// gitlabCredentialForUser resolves one user's credential for the project host,
// used when a run names its responsible user.
func (s *Service) gitlabCredentialForUser(project string, userID int64) (domain.GitHTTPCredentialSecret, string, string, bool) {
	platform, host, path, ok := domain.ExternalMRProject(project)
	if !ok || platform != domain.GitPlatformGitLab || userID <= 0 {
		return domain.GitHTTPCredentialSecret{}, host, path, false
	}
	creds, ok := s.projectAuth.(GitHTTPCredentialStore)
	if !ok || s.projectAuth == nil {
		return domain.GitHTTPCredentialSecret{}, host, path, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if cred, ok := creds.GitHTTPCredentialForHost(ctx, userID, host); ok && cred.Platform == domain.GitPlatformGitLab {
		return cred, host, path, true
	}
	return domain.GitHTTPCredentialSecret{}, host, path, false
}

func (s *Service) newGitLabClient(project string, cred domain.GitHTTPCredentialSecret, path string) *gitlabClient {
	base := cred.APIBase()
	client := gitegress.Client(s.gitHTTPSLookup())
	if s.gitHostAPIBase != "" {
		base, client = s.gitHostAPIBase, http.DefaultClient
	}
	return &gitlabClient{base: base, path: path, cred: cred, http: client}
}

func (g *gitlabClient) do(method, suffix string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, g.base+suffix, body)
	if err != nil {
		return err
	}
	req.Header.Set(g.cred.APIAuthHeader())
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", g.cred.Host, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var payload struct {
			Message any    `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&payload)
		detail := strings.TrimSpace(payload.Error)
		if detail == "" && payload.Message != nil {
			detail = strings.TrimSpace(fmt.Sprint(payload.Message))
		}
		return &gitlabHTTPError{Status: res.StatusCode, Host: g.cred.Host, Message: detail}
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(output)
}

func (g *gitlabClient) projectPath(suffix string) string {
	return "/projects/" + url.PathEscape(g.path) + suffix
}

type gitlabHTTPError struct {
	Status  int
	Host    string
	Message string
}

func (e *gitlabHTTPError) Error() string {
	detail := ""
	if e.Message != "" {
		detail = " (" + e.Message + ")"
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Sprintf("%s returned HTTP %d%s; check the owning user's Git access token and repository permissions", e.Host, e.Status, detail)
	case http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusConflict, http.StatusUnprocessableEntity:
		return fmt.Sprintf("%s refused the merge with HTTP %d%s: the merge request head moved or its mergeability is not computed yet, or it has a conflict; this is not a token problem", e.Host, e.Status, detail)
	default:
		return fmt.Sprintf("%s returned HTTP %d%s", e.Host, e.Status, detail)
	}
}

// gitlabMergeRaceStatus reports a merge refusal that can clear by itself:
// 409 (head moved), 405/406/422 (mergeability still computing or a merge in
// progress).
func gitlabMergeRaceStatus(err error) bool {
	var he *gitlabHTTPError
	return errors.As(err, &he) && (he.Status == http.StatusConflict ||
		he.Status == http.StatusMethodNotAllowed ||
		he.Status == http.StatusNotAcceptable ||
		he.Status == http.StatusUnprocessableEntity)
}

// gitlabMergeRetryDelays is the backoff between merge attempts after a
// head-moved or mergeability-pending refusal. Tests shorten it.
var gitlabMergeRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

type gitlabMR struct {
	IID          int        `json:"iid"`
	WebURL       string     `json:"web_url"`
	State        string     `json:"state"`
	MergedAt     *time.Time `json:"merged_at"`
	SourceBranch string     `json:"source_branch"`
	TargetBranch string     `json:"target_branch"`
	SHA          string     `json:"sha"`
	DiffRefs     struct {
		HeadSHA string `json:"head_sha"`
	} `json:"diff_refs"`
}

func (m gitlabMR) merged() bool { return m.State == "merged" || m.MergedAt != nil }

func (m gitlabMR) ref() string { return "!" + strconv.Itoa(m.IID) }

func (m gitlabMR) headSHA() string {
	if s := strings.TrimSpace(m.DiffRefs.HeadSHA); s != "" {
		return s
	}
	return strings.TrimSpace(m.SHA)
}

func (m gitlabMR) delivery(project string) domain.DeliveryMR {
	state := normalizeMRState(m.State)
	if m.merged() {
		state = "merged"
	}
	return domain.DeliveryMR{Project: project, IID: m.IID, URL: domain.GongfengMRURL(project, m.IID), State: state, GongfengState: state, TargetBranch: m.TargetBranch, HeadSHA: m.headSHA()}
}

func (m gitlabMR) mergedAt() time.Time {
	if m.MergedAt == nil {
		return time.Time{}
	}
	return m.MergedAt.UTC()
}

func (g *gitlabClient) listMRs(branch, target string) ([]gitlabMR, error) {
	q := url.Values{"source_branch": {branch}, "per_page": {"100"}, "order_by": {"updated_at"}, "sort": {"desc"}, "state": {"all"}}
	if target != "" {
		q.Set("target_branch", target)
	}
	var list []gitlabMR
	if err := g.do("GET", g.projectPath("/merge_requests?"+q.Encode()), nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (g *gitlabClient) findMR(branch, target string) (gitlabMR, bool, error) {
	list, err := g.listMRs(branch, target)
	if err != nil {
		return gitlabMR{}, false, err
	}
	for _, m := range list {
		if domain.SameGitBranch(m.SourceBranch, branch) && (target == "" || domain.SameGitBranch(m.TargetBranch, target)) {
			return m, true, nil
		}
	}
	return gitlabMR{}, false, nil
}

func gitlabMRsInto(list []gitlabMR, project, branch, target string) []domain.DeliveryMR {
	var out []domain.DeliveryMR
	for _, m := range list {
		if !domain.SameGitBranch(m.SourceBranch, branch) || !domain.SameGitBranch(m.TargetBranch, target) {
			continue
		}
		out = append(out, m.delivery(project))
	}
	return out
}

// gitlabPickBranchMR mirrors pickBranchMR: newest non-closed, else newest closed.
func gitlabPickBranchMR(list []gitlabMR, branch string) (gitlabMR, bool) {
	var best gitlabMR
	found, bestClosed := false, false
	for _, m := range list {
		if s := strings.TrimSpace(m.SourceBranch); s != "" && !domain.SameGitBranch(s, branch) {
			continue
		}
		closed := normalizeMRState(m.State) == "closed"
		switch {
		case !found:
		case bestClosed && !closed:
		case bestClosed == closed && m.IID > best.IID:
		default:
			continue
		}
		best, found, bestClosed = m, true, closed
	}
	return best, found
}

func (s *Service) gitlabFindBranch(project, branch string) (domain.DeliveryMR, bool, error) {
	cred, _, path, ok := s.gitlabCredentialForProject(project)
	if !ok {
		return domain.DeliveryMR{}, false, nil
	}
	g := s.newGitLabClient(project, cred, path)
	list, err := g.listMRs(branch, "")
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	m, ok := gitlabPickBranchMR(list, branch)
	if !ok {
		return domain.DeliveryMR{}, false, nil
	}
	return m.delivery(project), true, nil
}

func (s *Service) gitlabFindBranchInto(q domain.BranchMRQuery) ([]domain.DeliveryMR, error) {
	project, src, target := q.RemoteProject, strings.TrimSpace(q.SourceBranch), strings.TrimSpace(q.TargetBranch)
	cred, _, path, ok := s.gitlabCredentialForUser(project, q.ResponsibleUserID)
	if !ok {
		if cred, _, path, ok = s.gitlabCredentialForProject(project); !ok {
			return nil, fmt.Errorf("GitLab 仓库 %s 未找到可用的访问令牌（请责任人或项目 Owner 为该主机配置 Git 访问令牌）", project)
		}
	}
	g := s.newGitLabClient(project, cred, path)
	list, err := g.listMRs(src, target)
	if err != nil {
		return nil, err
	}
	return gitlabMRsInto(list, project, src, target), nil
}

func (s *Service) gitlabMergeBranch(project, branch, target, title, description string) (string, error) {
	if branch == "" {
		return "", errors.New("source branch is required")
	}
	if target != "" && branch == target {
		return "", nil
	}
	cred, _, path, ok := s.gitlabCredentialForProject(project)
	if !ok {
		return "", fmt.Errorf("GitLab 仓库 %s 未找到可用的访问令牌（请项目 Owner 为该主机配置 Git 访问令牌）", project)
	}
	g := s.newGitLabClient(project, cred, path)
	m, found, err := g.findMR(branch, target)
	if err != nil {
		return "", err
	}
	if found && m.merged() {
		return m.ref(), nil
	}
	if !found || m.State == "closed" {
		if target == "" {
			return "", ErrNoOpenMR
		}
		if title == "" {
			title = fmt.Sprintf("LoopForge: %s → %s", branch, target)
		}
		if description == "" {
			description = fmt.Sprintf("定时 SOP 将 `%s` 合入 `%s`。", branch, target)
		}
		if err := g.do("POST", g.projectPath("/merge_requests"), map[string]any{"source_branch": branch, "target_branch": target, "title": title, "description": description}, &m); err != nil {
			// A create race may have left a reusable open MR; search again.
			if again, ok, findErr := g.findMR(branch, target); findErr == nil && ok {
				m = again
			} else {
				return "", err
			}
		}
	}
	if m.merged() {
		return m.ref(), nil
	}
	if err := g.mergeMR(m); err != nil {
		return "", err
	}
	return m.ref(), nil
}

// mergeMR merges m with a merge commit, bound to its head sha. The work branch
// is often pushed right before this call, so GitLab may still report the
// previous head (409) or not have computed mergeability (405/406/422). Those
// refusals are retried against a freshly fetched MR.
func (g *gitlabClient) mergeMR(m gitlabMR) error {
	path := g.projectPath("/merge_requests/" + strconv.Itoa(m.IID))
	for attempt := 0; ; attempt++ {
		args := map[string]any{"squash": false}
		if sha := m.headSHA(); sha != "" {
			args["sha"] = sha
		}
		var result gitlabMR
		err := g.do("PUT", path+"/merge", args, &result)
		if err == nil {
			if !result.merged() {
				return fmt.Errorf("GitLab did not merge merge request !%d", m.IID)
			}
			return nil
		}
		if !gitlabMergeRaceStatus(err) {
			return err
		}
		if attempt >= len(gitlabMergeRetryDelays) {
			return fmt.Errorf("merge merge request !%d failed after %d attempts: %w", m.IID, attempt+1, err)
		}
		time.Sleep(gitlabMergeRetryDelays[attempt])
		var fresh gitlabMR
		if err := g.do("GET", path, nil, &fresh); err != nil {
			return err
		}
		if fresh.merged() {
			return nil
		}
		if fresh.State == "closed" {
			return fmt.Errorf("merge request !%d was closed before it could be merged", m.IID)
		}
		if fresh.headSHA() != "" {
			m.DiffRefs.HeadSHA, m.SHA = fresh.headSHA(), fresh.SHA
		}
	}
}
