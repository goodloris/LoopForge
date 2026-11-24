package domain

import "testing"

func TestExternalRepoInfoGitLab(t *testing.T) {
	const host = "code.example.net"
	accept := []struct {
		raw     string
		project string
		clone   string
	}{
		{"https://code.example.net/group/sub/proj", "gitlab:code.example.net/group/sub/proj", "https://code.example.net/group/sub/proj.git"},
		{"https://code.example.net/group/proj/-/merge_requests/3", "gitlab:code.example.net/group/proj", "https://code.example.net/group/proj.git"},
		{"https://code.example.net/group/proj.git", "gitlab:code.example.net/group/proj", "https://code.example.net/group/proj.git"},
		{"git@code.example.net:group/proj.git", "gitlab:code.example.net/group/proj", "https://code.example.net/group/proj.git"},
		{"ssh://git@code.example.net:2222/group/sub/proj.git", "gitlab:code.example.net/group/sub/proj", "https://code.example.net/group/sub/proj.git"},
		{"git clone https://code.example.net/group/proj.git", "gitlab:code.example.net/group/proj", "https://code.example.net/group/proj.git"},
	}
	for _, c := range accept {
		info, ok := ExternalRepoInfo(GitPlatformGitLab, host, c.raw)
		if !ok {
			t.Fatalf("ExternalRepoInfo(%q) rejected", c.raw)
		}
		if info.Project != c.project || info.CloneURL != c.clone {
			t.Fatalf("ExternalRepoInfo(%q) = %+v, want project=%q clone=%q", c.raw, info, c.project, c.clone)
		}
		// The MR project key round-trips through ExternalMRProject and GongfengMRURL.
		if platform, h, _, ok := ExternalMRProject(info.Project); !ok || platform != GitPlatformGitLab || h != host {
			t.Fatalf("ExternalMRProject(%q) = %q,%q ok=%v", info.Project, platform, h, ok)
		}
	}

	reject := []string{
		"https://evil.example.net/group/proj",     // other host, never send a token there
		"git@evil.example.net:group/proj.git",      // other host via ssh
		"https://code.example.net/onlygroup",       // single segment
		"https://user:pw@code.example.net/g/p",     // embedded credentials
		"https://github.com/owner/repo",            // different host
		"",
	}
	for _, raw := range reject {
		if info, ok := ExternalRepoInfo(GitPlatformGitLab, host, raw); ok {
			t.Fatalf("ExternalRepoInfo(%q) accepted an invalid/foreign ref: %+v", raw, info)
		}
	}

	// A host with an explicit non-default HTTPS port scopes and keys by it.
	if info, ok := ExternalRepoInfo(GitPlatformGitLab, "code.example.net:8443", "https://code.example.net:8443/group/proj"); !ok || info.Project != "gitlab:code.example.net:8443/group/proj" {
		t.Fatalf("ported host: %+v ok=%v", info, ok)
	}
	// Platform "other" has no API adapter, so it is not an external MR host.
	if _, ok := ExternalRepoInfo(GitPlatformOther, host, "https://code.example.net/group/proj"); ok {
		t.Fatal("platform other must not produce an MR project key")
	}
}
