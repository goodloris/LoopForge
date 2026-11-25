package domain

import (
	"net/url"
	"strings"
)

// ExternalRepoInfo builds the repository identity for a self-hosted Git host
// the administrator authorized (GitLab or GitHub Enterprise). The identity is
// the MR project key ("gitlab:<host>/<path>" or "ghe:<host>/<path>"), so SOP
// delivery, the merge-request gate and status polling all key off the same
// value the conversation workspace and status refresh already use. Clone and
// push use HTTPS with the per-host Git credential, so CloneURL is always the
// HTTPS form even when an ssh:// or git@ address was pasted.
//
// raw accepts https://host/group/.../repo(.git), a /-/ sub page, git@host:path
// and ssh://git@host/path. The host must equal the authorized host; any other
// host returns ok=false so a token is never sent elsewhere.
func ExternalRepoInfo(platform, host, raw string) (GongfengRepoInfo, bool) {
	platform, ok := NormalizeGitPlatform(platform)
	if !ok || platform == GitPlatformOther {
		return GongfengRepoInfo{}, false
	}
	host, err := HTTPSHostScope(host)
	if err != nil {
		return GongfengRepoInfo{}, false
	}
	parsedHost, path, ok := externalRepoHostPath(raw)
	if !ok || parsedHost != host {
		return GongfengRepoInfo{}, false
	}
	prefix := MRProjectGitLab
	if platform == GitPlatformGitHub {
		prefix = MRProjectGHE
	}
	project := prefix + host + "/" + path
	if _, _, _, valid := ExternalMRProject(project); !valid {
		return GongfengRepoInfo{}, false
	}
	base := "https://" + host + "/" + path
	name := path[strings.LastIndex(path, "/")+1:]
	return GongfengRepoInfo{Project: project, Name: name, WebURL: base, CloneURL: base + ".git"}, true
}

// externalRepoHostPath extracts the HTTPS host scope and the "group/.../repo"
// path from a pasted repository reference, dropping clone suffixes and sub
// pages. Credentials in the URL are rejected.
func externalRepoHostPath(raw string) (host, path string, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "git clone ")
	s = strings.Trim(s, "\"'")
	if s == "" || strings.ContainsAny(s, " \t\"'<>()\\") {
		return "", "", false
	}
	// scp-like git@host:group/repo.git
	if !strings.Contains(s, "://") && strings.HasPrefix(s, "git@") {
		rest := strings.TrimPrefix(s, "git@")
		h, p, found := strings.Cut(rest, ":")
		if !found {
			return "", "", false
		}
		scope, err := HTTPSHostScope(h)
		if err != nil {
			return "", "", false
		}
		np := normalizeExternalRepoPath(p)
		return scope, np, np != ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.User != nil {
			return "", "", false
		}
		scope, err := HTTPSHostScope(u.Host)
		if err != nil {
			return "", "", false
		}
		np := normalizeExternalRepoPath(u.Path)
		return scope, np, np != ""
	case "ssh", "git+ssh", "ssh+git":
		// An ssh port is not the HTTPS port; the API uses the host's default.
		scope, err := HTTPSHostScope(u.Hostname())
		if err != nil {
			return "", "", false
		}
		np := normalizeExternalRepoPath(u.Path)
		return scope, np, np != ""
	default:
		return "", "", false
	}
}

func normalizeExternalRepoPath(path string) string {
	p := strings.Trim(strings.TrimSpace(path), "/")
	if strings.HasSuffix(strings.ToLower(p), ".git") {
		p = p[:len(p)-4]
	}
	for _, marker := range gongfengRepoSubpages {
		if i := strings.Index(p, marker); i >= 0 {
			p = p[:i]
		}
	}
	p = strings.Trim(p, "/")
	if p == "" || !strings.Contains(p, "/") || strings.Contains(p, "..") {
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if !externalPathPart.MatchString(seg) || seg == "." || seg == ".." {
			return ""
		}
	}
	return p
}
