package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	// DetailedMergeStatus is GitLab 15.6+ (single-MR GET): "mergeable",
	// "ci_still_running", "ci_must_pass", "conflict", "checking", …
	DetailedMergeStatus string `json:"detailed_merge_status"`
	HeadPipeline        struct {
		Status string `json:"status"`
		WebURL string `json:"web_url"`
	} `json:"head_pipeline"`
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
			title = fmt.Sprintf("LooPool: %s → %s", branch, target)
		}
		if description == "" {
			description = fmt.Sprintf("定时 SOP 将 `%s` 合入 `%s`。", branch, target)
		}
		if err := g.do("POST", g.projectPath("/merge_requests"), map[string]any{"source_branch": branch, "target_branch": target, "title": title, "description": description, "remove_source_branch": false}, &m); err != nil {
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
		// A queued auto-merge is not a failure: report the MR reference so the
		// caller can record it; delivery polling advances it once CI passes.
		if errors.Is(err, domain.ErrMergeQueuedForPipeline) {
			return m.ref(), err
		}
		return "", err
	}
	return m.ref(), nil
}

// gitlabPipelineRunning / gitlabPipelineFailed classify a head pipeline status.
// GitLab pipeline states: created, waiting_for_resource, preparing, pending,
// running, success, failed, canceled, canceling, skipped, manual, scheduled.
func gitlabPipelineRunning(status string) bool {
	switch status {
	case "created", "waiting_for_resource", "preparing", "pending", "running", "scheduled", "manual":
		return true
	}
	return false
}

func gitlabPipelineFailed(status string) bool {
	return status == "failed" || status == "canceled" || status == "canceling"
}

// mergeMR merges m with a merge commit, keeping the source branch. The work
// branch is often pushed right before this call, so GitLab may still report
// the previous head (409) or not have computed mergeability (405/406/422);
// those are retried against a freshly fetched MR. When the project requires a
// passing pipeline (only_allow_merge_if_pipeline_succeeds) and the head
// pipeline is still running, the MR is set to merge automatically when the
// pipeline succeeds (merge_when_pipeline_succeeds) and a *domain.MergeQueuedError
// is returned — neither merged nor failed. A failed head pipeline returns a
// clear error with the pipeline link.
func (g *gitlabClient) mergeMR(m gitlabMR) error {
	path := g.projectPath("/merge_requests/" + strconv.Itoa(m.IID))
	headSHA := m.headSHA()
	var lastErr error
	for attempt := 0; ; attempt++ {
		// Read fresh state: list responses omit detailed_merge_status and
		// head_pipeline, and the head may have moved since the caller fetched.
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
		if sha := fresh.headSHA(); sha != "" {
			headSHA = sha
		}
		pipelineURL := fresh.HeadPipeline.WebURL
		ciGate := fresh.DetailedMergeStatus == "ci_still_running" || fresh.DetailedMergeStatus == "ci_must_pass"
		if ciGate && gitlabPipelineFailed(fresh.HeadPipeline.Status) {
			if pipelineURL == "" {
				return fmt.Errorf("merge request !%d cannot merge: its required pipeline did not pass (%s)", m.IID, fresh.DetailedMergeStatus)
			}
			return fmt.Errorf("merge request !%d cannot merge: its required pipeline did not pass: %s", m.IID, pipelineURL)
		}
		if ciGate && gitlabPipelineRunning(fresh.HeadPipeline.Status) {
			args := map[string]any{"merge_when_pipeline_succeeds": true, "should_remove_source_branch": false, "squash": false}
			if headSHA != "" {
				args["sha"] = headSHA
			}
			err := g.do("PUT", path+"/merge", args, nil)
			if err == nil {
				return &domain.MergeQueuedError{Ref: fresh.ref(), PipelineURL: pipelineURL}
			}
			if !gitlabMergeRaceStatus(err) {
				return err
			}
			lastErr = err
		} else {
			args := map[string]any{"squash": false, "should_remove_source_branch": false}
			if headSHA != "" {
				args["sha"] = headSHA
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
			lastErr = err
		}
		if attempt >= len(gitlabMergeRetryDelays) {
			// A persistent CI gate with a still-running pipeline queues rather
			// than failing, so the budget is only exhausted by genuine races.
			if ciGate {
				return fmt.Errorf("merge request !%d still cannot merge after %d attempts (%s)", m.IID, attempt+1, fresh.DetailedMergeStatus)
			}
			return fmt.Errorf("merge merge request !%d failed after %d attempts: %w", m.IID, attempt+1, lastErr)
		}
		time.Sleep(gitlabMergeRetryDelays[attempt])
	}
}

// EnsureBranchMR is the SOP delivery fallback: for a GitLab repository it
// returns the MR from sourceBranch, or opens one into targetBranch with the
// responsible user's (or project Owner's) per-host credential. Non-GitLab
// projects pass through with ok=false so the engine keeps GitHub/Gongfeng
// find-only behaviour. A missing credential is a clear error.
func (s *Service) EnsureBranchMR(project, sourceBranch, targetBranch string, userID int64) (domain.DeliveryMR, bool, error) {
	if !strings.HasPrefix(project, domain.MRProjectGitLab) {
		return domain.DeliveryMR{}, false, nil
	}
	project, src, target := strings.TrimSpace(project), strings.TrimSpace(sourceBranch), strings.TrimSpace(targetBranch)
	if src == "" {
		return domain.DeliveryMR{}, false, nil
	}
	cred, _, path, ok := s.gitlabCredentialForUser(project, userID)
	if !ok {
		cred, _, path, ok = s.gitlabCredentialForProject(project)
	}
	if !ok {
		return domain.DeliveryMR{}, false, fmt.Errorf("GitLab 仓库 %s 未找到可用的访问令牌（请登记人/负责人或项目 Owner 为该主机配置 Git 访问令牌）", project)
	}
	g := s.newGitLabClient(project, cred, path)
	// A usable (open or merged) MR for the work branch is reused, not duplicated.
	if list, err := g.listMRs(src, ""); err != nil {
		return domain.DeliveryMR{}, false, err
	} else if m, ok := gitlabPickBranchMR(list, src); ok && normalizeMRState(m.State) != "closed" {
		return m.delivery(project), true, nil
	}
	if target == "" || domain.SameGitBranch(src, target) {
		return domain.DeliveryMR{}, false, fmt.Errorf("GitLab 仓库 %s 的工作分支 %s 没有可用目标分支，无法创建 MR", project, src)
	}
	var created gitlabMR
	title := fmt.Sprintf("LooPool: %s → %s", src, target)
	if err := g.do("POST", g.projectPath("/merge_requests"), map[string]any{"source_branch": src, "target_branch": target, "title": title, "remove_source_branch": false}, &created); err != nil {
		// A create race may have left a reusable MR; search again before failing.
		if again, ok, findErr := g.findMR(src, target); findErr == nil && ok {
			return again.delivery(project), true, nil
		}
		return domain.DeliveryMR{}, false, err
	}
	return created.delivery(project), true, nil
}

// refreshGitLabDelivery refreshes one MR's state with the project Owner's
// per-host credential. It is the fallback used when refreshExternalDelivery
// cannot (an SOP-delivered MR has no registering user), mirroring
// refreshGitHubDelivery for GitHub.
func (s *Service) refreshGitLabDelivery(store MRDeliveryStore, project string, iid int) error {
	cred, _, path, ok := s.gitlabCredentialForProject(project)
	if !ok {
		return fmt.Errorf("GitLab 仓库 %s 未找到项目 Owner 的访问令牌", project)
	}
	g := s.newGitLabClient(project, cred, path)
	var m gitlabMR
	if err := g.do("GET", g.projectPath("/merge_requests/"+strconv.Itoa(iid)), nil, &m); err != nil {
		return err
	}
	d := m.delivery(project)
	return store.UpdateMRDeliveryState(project, iid, d.GongfengState, d.TargetBranch, m.mergedAt(), time.Now().UTC())
}

// mrOnBranch reports whether the merge request's source head commit is
// contained in branch (the GitLab equivalent of githubClient.pullOnBranch).
func (g *gitlabClient) mrOnBranch(iid int, branch string) (onBranch, ok bool) {
	var m gitlabMR
	if err := g.do("GET", g.projectPath("/merge_requests/"+strconv.Itoa(iid)), nil, &m); err != nil {
		return false, false
	}
	sha := m.headSHA()
	if sha == "" {
		return false, false
	}
	const pageSize = 100
	for page := 1; page <= 20; page++ {
		var refs []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		suffix := g.projectPath("/repository/commits/" + url.PathEscape(sha) + "/refs?type=branch&per_page=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(page))
		if err := g.do("GET", suffix, nil, &refs); err != nil {
			return false, false
		}
		names := make([]string, 0, len(refs))
		for _, ref := range refs {
			names = append(names, ref.Name)
		}
		if domain.BranchListed(names, branch) {
			return true, true
		}
		if len(refs) < pageSize {
			return false, true
		}
	}
	return false, false
}

// recordGitLabPromoteMRs stores merged integrate → default MRs as baseline
// promote rows, like recordGitHubPromotePulls does for GitHub.
func (s *Service) recordGitLabPromoteMRs(store MRDeliveryStore, g *gitlabClient, project, integ, def string) {
	if integ == "" || domain.SameGitBranch(integ, def) {
		return
	}
	list, err := g.listMRs(integ, def)
	if err != nil {
		log.Printf("mr-delivery discover promote %s: %v", project, err)
		return
	}
	now := time.Now().UTC()
	for _, m := range list {
		if !m.merged() || !domain.SameGitBranch(m.SourceBranch, integ) || !domain.SameGitBranch(m.TargetBranch, def) {
			continue
		}
		if err := store.EnsureMRDeliveryState(project, m.IID, domain.GongfengMRURL(project, m.IID), domain.BaselinePromoteIssueID); err != nil {
			log.Printf("mr-delivery ensure promote %s!%d: %v", project, m.IID, err)
			continue
		}
		if err := store.UpdateMRDeliveryState(project, m.IID, "merged", def, m.mergedAt(), now); err != nil {
			log.Printf("mr-delivery update promote %s!%d: %v", project, m.IID, err)
		}
	}
}

// syncGitLabWorkCommitsOnDefaultBranch is the GitLab side of
// syncWorkCommitsOnDefaultBranch: a merge request merged into the integrate
// branch counts as delivered once its head commit is contained in the default
// branch (the integrate branch was promoted). Promote MRs date the landing.
func (s *Service) syncGitLabWorkCommitsOnDefaultBranch(store MRDeliveryStore, project, def, integ string) {
	rows, err := store.MergedWorkMRs(project)
	if err != nil {
		log.Printf("mr-delivery work commits %s: %v", project, err)
		return
	}
	cred, _, path, ok := s.gitlabCredentialForProject(project)
	if !ok {
		return
	}
	g := s.newGitLabClient(project, cred, path)
	promotesListed := false
	for _, row := range rows {
		if row.MRIID <= 0 || domain.IsBaselinePromoteIssueID(row.IssueID) || domain.IsMainlineBranch(row.TargetBranch, def) {
			continue
		}
		cacheKey := project + "#" + strconv.Itoa(row.MRIID)
		if _, seen := s.landedOnDefault.Load(cacheKey); seen {
			continue
		}
		if !promotesListed {
			promotesListed = true
			s.recordGitLabPromoteMRs(store, g, project, integ, def)
		}
		onDefault, ok := g.mrOnBranch(row.MRIID, def)
		if !ok {
			continue
		}
		if err := store.ApplyDefaultBranchLanding(row.IssueID, row.MRURL, onDefault); err != nil {
			log.Printf("mr-delivery landing %s!%d: %v", project, row.MRIID, err)
			continue
		}
		if onDefault {
			s.landedOnDefault.Store(cacheKey, true)
		}
	}
}
