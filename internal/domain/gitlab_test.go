package domain

import "testing"

func TestGitLabDisabledByDefault(t *testing.T) {
	SetGitLabInstance("")
	if GitLabEnabled() {
		t.Fatal("GitLab must be disabled without an instance")
	}
	if _, ok := ParseGitLabRepoURL("https://code.example.net/group/proj"); ok {
		t.Fatal("disabled GitLab must not parse repository URLs")
	}
	if GitLabMRURL("gitlab:group/proj", 5) != "" {
		t.Fatal("disabled GitLab must not emit MR URLs")
	}
}

func TestGitLabRepoURLParsing(t *testing.T) {
	SetGitLabInstance("https://code.example.net")
	t.Cleanup(func() { SetGitLabInstance("") })

	accept := []struct {
		raw      string
		project  string
		web      string
		clone    string
		isSSH    bool
	}{
		{"https://code.example.net/group/sub/proj", "gitlab:group/sub/proj", "https://code.example.net/group/sub/proj", "https://code.example.net/group/sub/proj.git", false},
		{"https://code.example.net/group/proj/-/tree/main", "gitlab:group/proj", "https://code.example.net/group/proj", "https://code.example.net/group/proj.git", false},
		{"https://code.example.net/group/proj.git", "gitlab:group/proj", "https://code.example.net/group/proj", "https://code.example.net/group/proj.git", false},
		{"code.example.net/group/proj", "gitlab:group/proj", "https://code.example.net/group/proj", "https://code.example.net/group/proj.git", false},
		{"git@code.example.net:group/proj.git", "gitlab:group/proj", "https://code.example.net/group/proj", "git@code.example.net:group/proj.git", true},
		{"ssh://git@code.example.net:22/group/sub/proj.git", "gitlab:group/sub/proj", "https://code.example.net/group/sub/proj", "git@code.example.net:group/sub/proj.git", true},
		{"gitlab:code.example.net/group/proj", "gitlab:group/proj", "https://code.example.net/group/proj", "https://code.example.net/group/proj.git", false},
	}
	for _, c := range accept {
		info, ok := ParseGitLabRepoURL(c.raw)
		if !ok {
			t.Fatalf("ParseGitLabRepoURL(%q) rejected", c.raw)
		}
		if info.Project != c.project || info.WebURL != c.web || info.CloneURL != c.clone {
			t.Fatalf("ParseGitLabRepoURL(%q) = %+v, want project=%q web=%q clone=%q", c.raw, info, c.project, c.web, c.clone)
		}
		// ParseRepositoryURL must route the same inputs to the gitlab provider.
		if got, ok := ParseRepositoryURL(c.raw); !ok || got.Project != c.project {
			t.Fatalf("ParseRepositoryURL(%q) = %+v ok=%v, want project %q", c.raw, got, ok, c.project)
		}
	}

	reject := []string{
		"https://github.com/owner/repo",
		"https://evil.example.net/group/proj",
		"git@evil.example.net:group/proj.git",
		"group/proj",         // bare path: host not explicit
		"https://code.example.net/onlygroup", // single segment
		"",
	}
	for _, raw := range reject {
		if info, ok := ParseGitLabRepoURL(raw); ok {
			t.Fatalf("ParseGitLabRepoURL(%q) accepted a non-instance/invalid ref: %+v", raw, info)
		}
	}
}

func TestGitLabMRURLRoundTrip(t *testing.T) {
	SetGitLabInstance("https://code.example.net")
	t.Cleanup(func() { SetGitLabInstance("") })

	url := GongfengMRURL("gitlab:group/sub/proj", 5)
	if want := "https://code.example.net/group/sub/proj/-/merge_requests/5"; url != want {
		t.Fatalf("GongfengMRURL = %q, want %q", url, want)
	}
	refs := ParseGongfengMRs("delivered " + url + " done")
	if len(refs) != 1 || refs[0].Project != "gitlab:group/sub/proj" || refs[0].IID != 5 || refs[0].URL != url {
		t.Fatalf("ParseGongfengMRs round-trip = %+v", refs)
	}
	// The older /merge_requests/ form (no /-/) is also recognized.
	older := "https://code.example.net/group/proj/merge_requests/8"
	refs = ParseGongfengMRs(older)
	if len(refs) != 1 || refs[0].Project != "gitlab:group/proj" || refs[0].IID != 8 {
		t.Fatalf("ParseGongfengMRs(older form) = %+v", refs)
	}
	if got := GitLabCommitURL("gitlab:group/proj", "abc123"); got != "https://code.example.net/group/proj/-/commit/abc123" {
		t.Fatalf("GitLabCommitURL = %q", got)
	}
	// A merge request URL on another host must not be recognized as this
	// instance's MR.
	if refs := ParseGongfengMRs("https://evil.example.net/a/b/-/merge_requests/1"); len(refs) != 0 {
		t.Fatalf("foreign-host MR must be ignored: %+v", refs)
	}
}
