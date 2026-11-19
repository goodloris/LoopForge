package tracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// gitlabClient talks to the configured self-hosted GitLab instance over its
// v4 REST API, in the same direct net/http style as githubClient. The project
// is addressed by its URL-encoded path (":id"); a personal access token with
// the "api" scope authenticates through the PRIVATE-TOKEN header.
type gitlabClient struct {
	base, path, token string
	http              *http.Client
}

// gitlabBase resolves the API root: a test override, else the configured
// instance's /api/v4 root. Empty means GitLab is not configured.
func (s *Service) gitlabBase() string {
	if b := strings.TrimSpace(s.gitlabAPIBase); b != "" {
		return b
	}
	return domain.GitLabAPIBase()
}

func (s *Service) gitlabClient(project string) (*gitlabClient, error) {
	path := strings.TrimPrefix(strings.TrimSpace(project), "gitlab:")
	if path == "" || strings.ContainsAny(path, " \t\"'<>") || strings.Contains(path, "://") {
		return nil, fmt.Errorf("invalid GitLab repository")
	}
	base := s.gitlabBase()
	if base == "" {
		return nil, fmt.Errorf("GitLab 实例未配置（LOOPFORGE_GITLAB_URL）")
	}
	var token string
	if s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != "gitlab" || repo.GongfengProjectID != project {
				continue
			}
			if p, found := s.projectAuth.Project(repo.ProjectID); found {
				if u, exists := s.projectAuth.User(p.OwnerID); exists {
					token = gitlabAPIToken(u)
				}
			}
			break
		}
	}
	return &gitlabClient{base: base, path: path, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// gitlabClientForRun authenticates as the run did: its push token when set,
// else the Owner of the run's project when that project binds the repository.
// It also names the identity it chose.
func (s *Service) gitlabClientForRun(project, localProjectID, gitToken string) (*gitlabClient, string, error) {
	g, err := s.gitlabClient(project)
	if err != nil {
		return nil, "", err
	}
	if token := gitlabTokenFromCredential(gitToken); token != "" {
		g.token = token
		return g, "本轮责任人的 GitLab Token", nil
	}
	local := strings.TrimSpace(localProjectID)
	if s.projectAuth == nil || local == "" {
		return g, "首个绑定该仓库的项目 Owner 的 GitLab Token", nil
	}
	for _, repo := range s.projectAuth.Repositories() {
		if repo.Provider != "gitlab" || repo.GongfengProjectID != project || repo.ProjectID != local {
			continue
		}
		g.token = ""
		if p, found := s.projectAuth.Project(local); found {
			if u, exists := s.projectAuth.User(p.OwnerID); exists {
				g.token = gitlabAPIToken(u)
			}
		}
		return g, "项目 " + local + " Owner 的 GitLab Token", nil
	}
	return g, "首个绑定该仓库的项目 Owner 的 GitLab Token", nil
}

// gitlabAPIToken is the raw PAT used for REST calls: the user's GitLab token
// with any "oauth2:" git-credential prefix stripped.
func gitlabAPIToken(u domain.User) string {
	return gitlabTokenFromCredential(u.GitLabToken)
}

func gitlabTokenFromCredential(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, ':'); i >= 0 && strings.EqualFold(raw[:i], "oauth2") {
		return strings.TrimSpace(raw[i+1:])
	}
	return raw
}

func (g *gitlabClient) projectURL(suffix string) string {
	return g.base + "/projects/" + gitlabEncodeProjectPath(g.path) + suffix
}

// gitlabEncodeProjectPath URL-encodes a project path for use as the GitLab
// ":id" path segment: every "/" becomes "%2F" (url.PathEscape leaves "/"
// untouched), and other reserved characters are escaped too.
func gitlabEncodeProjectPath(path string) string {
	return strings.ReplaceAll(url.PathEscape(path), "/", "%2F")
}

func (g *gitlabClient) request(method, suffix string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, g.projectURL(suffix), body)
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/json")
	if g.token != "" {
		r.Header.Set("PRIVATE-TOKEN", g.token)
	}
	res, err := g.http.Do(r)
	if err != nil {
		return fmt.Errorf("GitLab request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var payload struct {
			Message any    `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&payload)
		msg := strings.TrimSpace(payload.Error)
		if msg == "" && payload.Message != nil {
			msg = strings.TrimSpace(fmt.Sprint(payload.Message))
		}
		return &gitlabHTTPError{Status: res.StatusCode, Message: msg}
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(output)
}

// gitlabHTTPError is a non-2xx GitLab response. Authentication and visibility
// failures point at the token; merge-state answers do not.
type gitlabHTTPError struct {
	Status  int
	Message string
}

func (e *gitlabHTTPError) Error() string {
	detail := ""
	if e.Message != "" {
		detail = " (" + e.Message + ")"
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Sprintf("GitLab returned HTTP %d%s; check the project owner's GitLab token and repository permissions", e.Status, detail)
	case http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusConflict, http.StatusUnprocessableEntity:
		return fmt.Sprintf("GitLab refused the merge with HTTP %d%s: the merge request head moved or its mergeability is not computed yet, or it has a merge conflict; this is not a token problem", e.Status, detail)
	default:
		return fmt.Sprintf("GitLab returned HTTP %d%s", e.Status, detail)
	}
}

// gitlabMergeRaceStatus reports a merge refusal that can clear by itself:
// 409 when the sha no longer matches the head, 405/406/422 while GitLab is
// still computing mergeability or a merge is in progress.
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
	ID           int        `json:"id"`
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

// merged reports whether GitLab considers the merge request merged.
func (m gitlabMR) merged() bool { return m.State == "merged" || m.MergedAt != nil }

// ref is the "!N" reference returned to delivery callers.
func (m gitlabMR) ref() string { return "!" + strconv.Itoa(m.IID) }

// headSHA is the merge request's source head commit the host reports.
func (m gitlabMR) headSHA() string {
	if s := strings.TrimSpace(m.DiffRefs.HeadSHA); s != "" {
		return s
	}
	return strings.TrimSpace(m.SHA)
}

func (m gitlabMR) delivery(project string) domain.DeliveryMR {
	state := "opened"
	if m.merged() {
		state = "merged"
	} else if m.State == "closed" {
		state = "closed"
	}
	return domain.DeliveryMR{Project: project, IID: m.IID, URL: domain.GongfengMRURL(project, m.IID), State: state, GongfengState: state, TargetBranch: m.TargetBranch}
}

// mergedAt is GitLab's merged_at, never the poll time; zero until merged.
func (m gitlabMR) mergedAt() time.Time {
	if m.MergedAt == nil {
		return time.Time{}
	}
	return m.MergedAt.UTC()
}

// listMRs returns the merge requests from branch (and into target when set),
// most recently updated first, across all states.
func (g *gitlabClient) listMRs(branch, target string) ([]gitlabMR, error) {
	q := url.Values{"source_branch": {branch}, "per_page": {"100"}, "order_by": {"updated_at"}, "sort": {"desc"}, "state": {"all"}}
	if target != "" {
		q.Set("target_branch", target)
	}
	var list []gitlabMR
	if err := g.request("GET", "/merge_requests?"+q.Encode(), nil, &list); err != nil {
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
		mr := m.delivery(project)
		mr.HeadSHA = m.headSHA()
		out = append(out, mr)
	}
	return out
}

func (s *Service) gitlabFindBranch(project, branch string) (domain.DeliveryMR, bool, error) {
	g, err := s.gitlabClient(project)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	m, ok, err := g.findMR(branch, "")
	if err != nil || !ok {
		return domain.DeliveryMR{}, ok, err
	}
	return m.delivery(project), true, nil
}

func (s *Service) gitlabMergeBranch(project, branch, target, title, description string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("source branch is required")
	}
	if branch == target {
		return "", nil
	}
	g, err := s.gitlabClient(project)
	if err != nil {
		return "", err
	}
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
			title = "LoopForge: " + branch + " → " + target
		}
		if err := g.request("POST", "/merge_requests", map[string]any{"source_branch": branch, "target_branch": target, "title": title, "description": description}, &m); err != nil {
			return "", err
		}
	}
	if err := g.mergeMR(m); err != nil {
		return "", err
	}
	return m.ref(), nil
}

// mergeMR merges m with a merge commit, bound to its head sha. The work branch
// is usually pushed right before this call, so GitLab may still report the
// previous head (409) or not have computed mergeability (405/406/422). Those
// refusals are retried with backoff against a freshly fetched merge request.
func (g *gitlabClient) mergeMR(m gitlabMR) error {
	path := "/merge_requests/" + strconv.Itoa(m.IID)
	for attempt := 0; ; attempt++ {
		args := map[string]any{"squash": false}
		if sha := m.headSHA(); sha != "" {
			args["sha"] = sha
		}
		var result gitlabMR
		err := g.request("PUT", path+"/merge", args, &result)
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
		if err := g.request("GET", path, nil, &fresh); err != nil {
			return err
		}
		if fresh.merged() {
			return nil
		}
		if fresh.State == "closed" {
			return fmt.Errorf("merge request !%d was closed before it could be merged", m.IID)
		}
		if fresh.headSHA() != "" {
			m.DiffRefs.HeadSHA = fresh.headSHA()
			m.SHA = fresh.SHA
		}
	}
}

func (s *Service) refreshGitLabDelivery(store MRDeliveryStore, project string, number int) error {
	g, err := s.gitlabClient(project)
	if err != nil {
		return err
	}
	var m gitlabMR
	if err := g.request("GET", "/merge_requests/"+strconv.Itoa(number), nil, &m); err != nil {
		return err
	}
	d := m.delivery(project)
	return store.UpdateMRDeliveryState(project, number, d.GongfengState, d.TargetBranch, m.mergedAt(), time.Now().UTC())
}

// GitLabFileOnBranch reports whether path exists on branch, read with the
// Owner's token. GitLab answers 404 both for a missing file and for a project
// the token cannot see, so a 404 counts as missing only when the project
// itself is visible; otherwise the error is returned and callers treat the
// answer as unknown.
func (s *Service) GitLabFileOnBranch(project, branch, path string) (bool, error) {
	g, err := s.gitlabClient(project)
	if err != nil {
		return false, err
	}
	suffix := "/repository/files/" + url.PathEscape(path) + "?ref=" + url.QueryEscape(branch)
	err = g.request(http.MethodGet, suffix, nil, nil)
	var httpErr *gitlabHTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		return err == nil, err
	}
	if repoErr := g.request(http.MethodGet, "", nil, nil); repoErr != nil {
		return false, repoErr
	}
	return false, nil
}

// syncGitLabWorkCommitsOnDefaultBranch is the GitLab side of
// syncWorkCommitsOnDefaultBranch: a merge request merged into the integrate
// branch counts as delivered once its head commit is contained in the default
// branch (the integrate branch was promoted).
func (s *Service) syncGitLabWorkCommitsOnDefaultBranch(store MRDeliveryStore, project, def, integ string) {
	rows, err := store.MergedWorkMRs(project)
	if err != nil {
		log.Printf("mr-delivery work commits %s: %v", project, err)
		return
	}
	var g *gitlabClient
	promotesListed := false
	for _, row := range rows {
		if row.MRIID <= 0 || domain.IsBaselinePromoteIssueID(row.IssueID) || domain.IsMainlineBranch(row.TargetBranch, def) {
			continue
		}
		cacheKey := project + "#" + strconv.Itoa(row.MRIID)
		if _, seen := s.landedOnDefault.Load(cacheKey); seen {
			continue
		}
		if g == nil {
			if g, err = s.gitlabClient(project); err != nil {
				log.Printf("mr-delivery work commits %s: %v", project, err)
				return
			}
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

// recordGitLabPromoteMRs stores merged integrate → default MRs as baseline
// promote rows, like discoverPromoteMRs does for Gongfeng.
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

// mrOnBranch reports whether the merge request's head commit is contained in
// branch. ok is false when GitLab could not answer.
func (g *gitlabClient) mrOnBranch(iid int, branch string) (onBranch, ok bool) {
	var m gitlabMR
	if err := g.request("GET", "/merge_requests/"+strconv.Itoa(iid), nil, &m); err != nil {
		log.Printf("mr-delivery mr %s!%d: %v", g.path, iid, err)
		return false, false
	}
	sha := m.headSHA()
	if sha == "" {
		return false, false
	}
	const pageSize = 100
	for page := 1; page <= 20; page++ {
		var refs []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		suffix := "/repository/commits/" + url.PathEscape(sha) + "/refs?type=branch&per_page=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(page)
		if err := g.request("GET", suffix, nil, &refs); err != nil {
			log.Printf("mr-delivery commit refs %s %s: %v", g.path, sha, err)
			return false, false
		}
		names := make([]string, 0, len(refs))
		for _, r := range refs {
			names = append(names, r.Name)
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
