package domain

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// GitLab support is scoped to a single, self-hosted instance configured by
// LOOPFORGE_GITLAB_URL. The instance host is resolved once at startup through
// SetGitLabInstance and consulted by the pure parsing/emission helpers here.
// When no instance is configured, GitLab repositories, links and the gitlab
// provider option are all inert: ParseGitLabRepoURL rejects everything and
// GitLabMRURL returns "".
var (
	gitlabMu      sync.RWMutex
	gitlabHost    string // lowercased host, e.g. code.example.net
	gitlabWebBase string // scheme://host, no trailing slash
	gitlabMRRe    *regexp.Regexp
)

// SetGitLabInstance publishes the configured GitLab instance. rawURL is the
// deployment's LOOPFORGE_GITLAB_URL (scheme + host, any path is ignored). An
// empty or unparseable value disables GitLab. It is safe to call at startup
// and from tests.
func SetGitLabInstance(rawURL string) {
	gitlabMu.Lock()
	defer gitlabMu.Unlock()
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		gitlabHost, gitlabWebBase, gitlabMRRe = "", "", nil
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		gitlabHost, gitlabWebBase, gitlabMRRe = "", "", nil
		return
	}
	gitlabHost = strings.ToLower(u.Host)
	gitlabWebBase = u.Scheme + "://" + u.Host
	// A merge request link on this instance: <base>/<path>/-/merge_requests/N
	// or the older <base>/<path>/merge_requests/N. The path (non-greedy) may
	// carry sub-groups. Either http or https scheme is accepted in free text.
	escaped := regexp.QuoteMeta(strings.ToLower(u.Host))
	gitlabMRRe = regexp.MustCompile(`(?i)https?://` + escaped + `/([^\s"'<>#?]+?)/(?:-/)?merge_requests/(\d+)`)
}

// GitLabEnabled reports whether a GitLab instance is configured.
func GitLabEnabled() bool {
	gitlabMu.RLock()
	defer gitlabMu.RUnlock()
	return gitlabHost != ""
}

// GitLabHost returns the configured instance host, or "" when disabled.
func GitLabHost() string {
	gitlabMu.RLock()
	defer gitlabMu.RUnlock()
	return gitlabHost
}

// GitLabWebBase returns scheme://host of the configured instance, or "".
func GitLabWebBase() string {
	gitlabMu.RLock()
	defer gitlabMu.RUnlock()
	return gitlabWebBase
}

// GitLabAPIBase returns the v4 REST API root of the configured instance
// (scheme://host/api/v4), or "" when disabled.
func GitLabAPIBase() string {
	base := GitLabWebBase()
	if base == "" {
		return ""
	}
	return base + "/api/v4"
}

// gitLabProjectPath returns the "gitlab:" project identity's bare path.
func gitLabProjectPath(project string) string {
	return strings.TrimPrefix(strings.TrimSpace(project), "gitlab:")
}

// IsGitLabProject reports whether a remote project identity targets GitLab.
func IsGitLabProject(project string) bool {
	return strings.HasPrefix(strings.TrimSpace(project), "gitlab:")
}

// GitLabMRURL is the canonical web URL of a merge request on the configured
// instance. It returns "" when GitLab is disabled or the inputs are invalid.
func GitLabMRURL(project string, iid int) string {
	base := GitLabWebBase()
	path := strings.Trim(gitLabProjectPath(project), "/")
	if base == "" || path == "" || iid <= 0 {
		return ""
	}
	return base + "/" + path + "/-/merge_requests/" + strconv.Itoa(iid)
}

// GitLabCommitURL is the canonical web URL of a commit on the configured
// instance. It returns "" when GitLab is disabled or the inputs are invalid.
func GitLabCommitURL(project, sha string) string {
	base := GitLabWebBase()
	path := strings.Trim(gitLabProjectPath(project), "/")
	sha = strings.TrimSpace(sha)
	if base == "" || path == "" || sha == "" {
		return ""
	}
	return base + "/" + path + "/-/commit/" + sha
}

// parseGitLabMRs extracts the merge requests of the configured instance from
// free text, as gitlab: project identities, in appearance order.
func parseGitLabMRs(text string) []GongfengMRRef {
	gitlabMu.RLock()
	re := gitlabMRRe
	gitlabMu.RUnlock()
	if re == nil {
		return nil
	}
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]GongfengMRRef, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		path := normalizeGitLabProjectPath(match[1])
		iid, _ := strconv.Atoi(match[2])
		if path == "" || iid <= 0 {
			continue
		}
		project := "gitlab:" + path
		key := gongfengMRKey(project, iid)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, GongfengMRRef{Project: project, IID: iid, URL: GitLabMRURL(project, iid)})
	}
	return out
}

// ParseGitLabRepoURL parses a repository reference on the configured GitLab
// instance into its project identity ("gitlab:<group/.../project>") and
// normalized URLs. It rejects any other host so a personal access token can
// never be sent to a different server. Accepted inputs mirror the Gongfeng
// parser (web, /-/ sub pages, .git, scp-like git@, ssh://), with sub-groups
// allowed. A bare "group/project" without a host is intentionally rejected:
// the instance host must be explicit to attribute a paste to GitLab.
func ParseGitLabRepoURL(raw string) (GongfengRepoInfo, bool) {
	host := GitLabHost()
	base := GitLabWebBase()
	if host == "" {
		return GongfengRepoInfo{}, false
	}
	s := strings.Trim(strings.TrimSpace(raw), `"'`)
	if len(s) >= 10 && strings.EqualFold(s[:10], "git clone ") {
		s = strings.Trim(strings.TrimSpace(s[10:]), `"'`)
		if i := strings.IndexAny(s, " \t"); i >= 0 {
			s = s[:i]
		}
	}
	s = strings.TrimPrefix(s, "gitlab:")
	if s == "" || strings.ContainsAny(s, " \t\"'<>()") {
		return GongfengRepoInfo{}, false
	}
	sshClone := strings.HasPrefix(strings.ToLower(s), "git@") || strings.HasPrefix(strings.ToLower(s), "ssh://")
	s = strings.TrimSuffix(s, "/")
	// Strip scheme and userinfo so ssh / https / scp forms converge on host/path.
	pathSource := s
	if i := strings.Index(pathSource, "://"); i >= 0 {
		pathSource = pathSource[i+3:]
	}
	if i := strings.LastIndex(pathSource, "@"); i >= 0 {
		pathSource = pathSource[i+1:]
	}
	if !strings.Contains(pathSource, "/") && !strings.Contains(pathSource, ":") {
		// Bare host or bare path: not an attributable GitLab reference.
		return GongfengRepoInfo{}, false
	}
	hostPart := pathSource[:strings.IndexAny(pathSource, "/:")]
	if !strings.EqualFold(hostPart, host) {
		return GongfengRepoInfo{}, false
	}
	var path string
	if pathSource[len(hostPart)] == ':' {
		// git@host:group/repo.git (scp) or ssh://git@host:22/group/repo.git
		path = pathSource[len(hostPart)+1:]
		if j := strings.Index(path, "/"); j >= 0 && isAllDigits(path[:j]) {
			path = path[j+1:]
		}
	} else {
		path = pathSource[len(hostPart)+1:]
	}
	project := normalizeGitLabProjectPath(path)
	if project == "" {
		return GongfengRepoInfo{}, false
	}
	info := GongfengRepoInfo{Project: "gitlab:" + project}
	info.Name = project[strings.LastIndex(project, "/")+1:]
	info.WebURL = base + "/" + project
	info.CloneURL = info.WebURL + ".git"
	if sshClone {
		info.CloneURL = "git@" + host + ":" + project + ".git"
	}
	return info, true
}

// normalizeGitLabProjectPath strips clone suffixes and sub page markers from a
// repository path and validates the remaining "group/.../project" shape.
// Sub-groups are allowed, so two or more segments pass.
func normalizeGitLabProjectPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return ""
	}
	if host := GitLabHost(); host != "" {
		p = strings.TrimPrefix(p, host+"/")
	}
	if strings.HasSuffix(strings.ToLower(p), ".git") {
		p = p[:len(p)-4]
	}
	for _, marker := range gongfengRepoSubpages {
		if i := strings.Index(p, marker); i >= 0 {
			p = p[:i]
		}
	}
	p = strings.Trim(p, "/")
	if p == "" || !strings.Contains(p, "/") {
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || strings.ContainsAny(seg, " \t\\?#") {
			return ""
		}
	}
	return p
}
