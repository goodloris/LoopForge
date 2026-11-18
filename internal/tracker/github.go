package tracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type githubClient struct {
	base, path, token string
	http              *http.Client
}

func (s *Service) githubClient(project string) (*githubClient, error) {
	path := strings.TrimPrefix(project, "github:")
	info, ok := domain.ParseRepositoryURL("https://github.com/" + path)
	if !ok || info.Project != project {
		return nil, fmt.Errorf("invalid GitHub repository")
	}
	var token string
	if s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != "github" || repo.GongfengProjectID != project {
				continue
			}
			if p, found := s.projectAuth.Project(repo.ProjectID); found {
				if u, exists := s.projectAuth.User(p.OwnerID); exists {
					token = u.GitToken
				}
			}
			break
		}
	}
	base := s.githubAPIBase
	if base == "" {
		base = "https://api.github.com"
	}
	return &githubClient{base: base, path: path, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// githubClientForRun authenticates as the run did: its push token when set,
// else the Owner of the run's project when that project binds the repository.
// It also names the identity it chose.
func (s *Service) githubClientForRun(project, localProjectID, gitToken string) (*githubClient, string, error) {
	g, err := s.githubClient(project)
	if err != nil {
		return nil, "", err
	}
	if token := strings.TrimSpace(gitToken); token != "" {
		g.token = token
		return g, "本轮责任人的 Git Token", nil
	}
	local := strings.TrimSpace(localProjectID)
	if s.projectAuth == nil || local == "" {
		return g, "首个绑定该仓库的项目 Owner 的 Git Token", nil
	}
	for _, repo := range s.projectAuth.Repositories() {
		if repo.Provider != "github" || repo.GongfengProjectID != project || repo.ProjectID != local {
			continue
		}
		g.token = ""
		if p, found := s.projectAuth.Project(local); found {
			if u, exists := s.projectAuth.User(p.OwnerID); exists {
				g.token = u.GitToken
			}
		}
		return g, "项目 " + local + " Owner 的 Git Token", nil
	}
	return g, "首个绑定该仓库的项目 Owner 的 Git Token", nil
}

func (g *githubClient) request(method, suffix string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, g.base+"/repos/"+g.path+suffix, body)
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "application/vnd.github+json")
	r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	r.Header.Set("Content-Type", "application/json")
	if g.token != "" {
		r.Header.Set("Authorization", "Bearer "+g.token)
	}
	res, err := g.http.Do(r)
	if err != nil {
		return fmt.Errorf("GitHub request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&payload)
		return &githubHTTPError{Status: res.StatusCode, Message: strings.TrimSpace(payload.Message)}
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(output)
}

// githubHTTPError is a non-2xx GitHub response. Only authentication and
// visibility failures point at the token; merge-state answers do not.
type githubHTTPError struct {
	Status  int
	Message string
}

func (e *githubHTTPError) Error() string {
	detail := ""
	if e.Message != "" {
		detail = " (" + e.Message + ")"
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Sprintf("GitHub returned HTTP %d%s; check the project owner's Git token and repository permissions", e.Status, detail)
	case http.StatusMethodNotAllowed, http.StatusConflict:
		return fmt.Sprintf("GitHub refused the merge with HTTP %d%s: the pull request head moved or its mergeability is not computed yet, or it has a merge conflict; this is not a token problem", e.Status, detail)
	default:
		return fmt.Sprintf("GitHub returned HTTP %d%s", e.Status, detail)
	}
}

// mergeRaceStatus reports a merge refusal that can clear by itself: 409 when
// the sha no longer matches the head, 405 while GitHub computes mergeability.
func mergeRaceStatus(err error) bool {
	var he *githubHTTPError
	return errors.As(err, &he) && (he.Status == http.StatusConflict || he.Status == http.StatusMethodNotAllowed)
}

// githubMergeRetryDelays is the backoff between merge attempts after a
// head-moved or mergeability-pending refusal. Tests shorten it.
var githubMergeRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

type githubPull struct {
	Number   int        `json:"number"`
	URL      string     `json:"html_url"`
	State    string     `json:"state"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	Head     struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// merged reports whether GitHub considers the pull request merged; either
// flag is enough because list responses may omit "merged".
func (p githubPull) merged() bool { return p.Merged || p.MergedAt != nil }

// ref is the "#N" reference returned to delivery callers.
func (p githubPull) ref() string { return "#" + strconv.Itoa(p.Number) }

func (p githubPull) delivery(project string) domain.DeliveryMR {
	state := "opened"
	if p.merged() {
		state = "merged"
	} else if p.State == "closed" {
		state = "closed"
	}
	return domain.DeliveryMR{Project: project, IID: p.Number, URL: domain.GongfengMRURL(project, p.Number), State: state, GongfengState: state, TargetBranch: p.Base.Ref}
}

// mergedAt is GitHub's merged_at, never the poll time; zero until merged.
func (p githubPull) mergedAt() time.Time {
	if p.MergedAt == nil {
		return time.Time{}
	}
	return p.MergedAt.UTC()
}

func (g *githubClient) findPull(branch, target string) (githubPull, bool, error) {
	list, err := g.listPulls(branch, target)
	if err != nil {
		return githubPull{}, false, err
	}
	for _, p := range list {
		if p.Head.Ref == branch && (target == "" || p.Base.Ref == target) {
			return p, true, nil
		}
	}
	return githubPull{}, false, nil
}

// listPulls returns the pulls from branch (and into target when set), most
// recently updated first.
func (g *githubClient) listPulls(branch, target string) ([]githubPull, error) {
	q := url.Values{"state": {"all"}, "head": {strings.Split(g.path, "/")[0] + ":" + branch}, "per_page": {"100"}, "sort": {"updated"}, "direction": {"desc"}}
	if target != "" {
		q.Set("base", target)
	}
	var list []githubPull
	if err := g.request("GET", "/pulls?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func githubPullsInto(pulls []githubPull, project, branch, target string) []domain.DeliveryMR {
	var out []domain.DeliveryMR
	for _, p := range pulls {
		if p.Head.Ref != branch || p.Base.Ref != target {
			continue
		}
		mr := p.delivery(project)
		mr.HeadSHA = strings.TrimSpace(p.Head.SHA)
		out = append(out, mr)
	}
	return out
}

func (s *Service) githubFindBranch(project, branch string) (domain.DeliveryMR, bool, error) {
	g, err := s.githubClient(project)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	p, ok, err := g.findPull(branch, "")
	if err != nil || !ok {
		return domain.DeliveryMR{}, ok, err
	}
	return p.delivery(project), true, nil
}

func (s *Service) githubMergeBranch(project, branch, target, title, description string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("source branch is required")
	}
	if branch == target {
		return "", nil
	}
	g, err := s.githubClient(project)
	if err != nil {
		return "", err
	}
	p, found, err := g.findPull(branch, target)
	if err != nil {
		return "", err
	}
	if found && p.merged() {
		return p.ref(), nil
	}
	if !found || p.State == "closed" {
		if target == "" {
			return "", ErrNoOpenMR
		}
		if title == "" {
			title = "LoopForge: " + branch + " → " + target
		}
		if err := g.request("POST", "/pulls", map[string]any{"head": branch, "base": target, "title": title, "body": description}, &p); err != nil {
			return "", err
		}
	}
	if err := g.mergePull(p); err != nil {
		return "", err
	}
	return p.ref(), nil
}

// mergePull merges p, bound to its head sha. The work branch is usually
// pushed right before this call, so GitHub may still report the previous head
// (409) or not have computed mergeability (405). Those refusals are retried
// with backoff against a freshly fetched pull request.
func (g *githubClient) mergePull(p githubPull) error {
	path := "/pulls/" + strconv.Itoa(p.Number)
	for attempt := 0; ; attempt++ {
		var result struct {
			Merged bool `json:"merged"`
		}
		err := g.request("PUT", path+"/merge", map[string]any{"merge_method": "merge", "sha": p.Head.SHA}, &result)
		if err == nil {
			if !result.Merged {
				return fmt.Errorf("GitHub did not merge pull request #%d", p.Number)
			}
			return nil
		}
		if !mergeRaceStatus(err) {
			return err
		}
		if attempt >= len(githubMergeRetryDelays) {
			return fmt.Errorf("merge pull request #%d failed after %d attempts: %w", p.Number, attempt+1, err)
		}
		time.Sleep(githubMergeRetryDelays[attempt])
		var fresh githubPull
		if err := g.request("GET", path, nil, &fresh); err != nil {
			return err
		}
		if fresh.merged() {
			return nil
		}
		if fresh.Head.SHA != "" {
			p.Head.SHA = fresh.Head.SHA
		}
	}
}

func (s *Service) pullGitHubIssues(store IssueStoreAdapter, repo domain.Repository) (int, error) {
	g, err := s.githubClient(repo.GongfengProjectID)
	if err != nil {
		return 0, err
	}
	count := 0
	for page := 1; page <= 100; page++ {
		var list []struct {
			ID          int64           `json:"id"`
			Number      int             `json:"number"`
			Title       string          `json:"title"`
			Body        string          `json:"body"`
			State       string          `json:"state"`
			URL         string          `json:"html_url"`
			CreatedAt   time.Time       `json:"created_at"`
			PullRequest json.RawMessage `json:"pull_request"`
		}
		if err := g.request("GET", "/issues?state=all&per_page=100&page="+strconv.Itoa(page), nil, &list); err != nil {
			return count, err
		}
		for _, item := range list {
			if len(item.PullRequest) > 0 {
				continue
			}
			id := fmt.Sprintf("gh-%s-%d", repo.ID, item.Number)
			// Number stays 0 so the store assigns the next project number; the
			// GitHub issue number is the external ID (ExternalRef.ExternalIID).
			issue := domain.Issue{ID: id, ProjectID: repo.ProjectID, RepositoryID: repo.ID, BaseBranch: repo.DefaultBranch, Kind: "issue", Title: item.Title, Description: item.Body, Priority: "medium", Source: "github", Status: gongfengStateToStatus(item.State), CreatedAt: item.CreatedAt, UpdatedAt: time.Now().UTC(), ExternalRef: &domain.ExternalRef{Source: "github", ExternalID: strconv.FormatInt(item.ID, 10), ExternalIID: item.Number, ExternalURL: item.URL, ExternalState: item.State, ExternalType: "issue", SyncedAt: time.Now().UTC()}}
			issue.ExternalRef.SourceContentHash = githubBodyHash(item.Body)
			if item.State == "open" {
				issue.Status = "todo"
			}
			if item.State == "closed" {
				issue.CloseSource = "external"
			}
			if old, ok := store.Issue(id); ok {
				applyExistingIssue(&issue, old)
				if old.Clarification.Status != "" || (old.ExternalRef != nil && githubBodyHash(old.Description) != old.ExternalRef.SourceContentHash) {
					issue.Description = old.Description
				}
			}
			if err := store.PutIssue(issue); err != nil {
				return count, err
			}
			count++
		}
		if len(list) < 100 {
			return count, nil
		}
	}
	return count, fmt.Errorf("GitHub import exceeded 100 pages; narrow the repository scope")
}

func githubBodyHash(body string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(body))) }

func (s *Service) refreshGitHubDelivery(store MRDeliveryStore, project string, number int) error {
	g, err := s.githubClient(project)
	if err != nil {
		return err
	}
	var p githubPull
	if err := g.request("GET", "/pulls/"+strconv.Itoa(number), nil, &p); err != nil {
		return err
	}
	d := p.delivery(project)
	return store.UpdateMRDeliveryState(project, number, d.GongfengState, d.TargetBranch, p.mergedAt(), time.Now().UTC())
}
