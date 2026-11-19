package config

// GitLabURL is the single self-hosted GitLab instance the deployment enables
// through LOOPFORGE_GITLAB_URL (for example https://code.example.net). It is
// the scheme + host of the instance; any path is ignored. An empty value
// leaves the gitlab provider disabled. Call domain.SetGitLabInstance with this
// value once at startup.
func GitLabURL() string { return envURL("LOOPFORGE_GITLAB_URL") }
