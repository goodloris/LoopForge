package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// Runner side of compose preview environments (preview_deploy /
// preview_teardown). Layout under Config.PreviewRoot:
//
//	previews/<envId>/{src/,override.yaml,state.json}
//	caddy/routes/<envId>.caddy   (imported by deploy/preview-edge/Caddyfile)
const (
	defaultPreviewRoot         = "/srv/lfpreview"
	defaultPreviewCaddy        = "lf-caddy"
	defaultPreviewAuthUpstream = "host.docker.internal:9001"
	previewWaitTimeoutSeconds  = "600"
	previewLogsTail            = "80"
	previewLogsLimit           = 6000
	previewOutputLimit         = 4000
)

// previewState is the runner's own record of a deployed environment, used
// for the same-SHA no-op.
type previewState struct {
	Status string `json:"status"`
	SHA    string `json:"sha"`
	Host   string `json:"host"`
	Port   int    `json:"port,omitempty"`
}

func (d *Daemon) previewDir(envID string) string {
	return filepath.Join(d.cfg.PreviewRoot, "previews", envID)
}

func (d *Daemon) previewRoutePath(envID string) string {
	return filepath.Join(d.cfg.PreviewRoot, "caddy", "routes", envID+".caddy")
}

// lockPreview serializes deploy and teardown of one environment.
func (d *Daemon) lockPreview(envID string) func() {
	value, _ := d.previewLocks.LoadOrStore(envID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (d *Daemon) executePreviewDeploy(ctx context.Context, task domain.RunnerTask) execResult {
	return d.executePreviewTask(ctx, task, func(spec domain.RunnerPreviewSpec) (domain.RunnerPreviewResult, error) {
		if !previewHostPattern.MatchString(spec.Host) {
			return domain.RunnerPreviewResult{}, fmt.Errorf("preview host %q is invalid", spec.Host)
		}
		return d.previewDeploy(ctx, task.Invocation, spec)
	})
}

func (d *Daemon) executePreviewTeardown(ctx context.Context, task domain.RunnerTask) execResult {
	return d.executePreviewTask(ctx, task, func(spec domain.RunnerPreviewSpec) (domain.RunnerPreviewResult, error) {
		return d.previewTeardown(ctx, spec.EnvID)
	})
}

func (d *Daemon) executePreviewTask(ctx context.Context, task domain.RunnerTask, run func(domain.RunnerPreviewSpec) (domain.RunnerPreviewResult, error)) execResult {
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start: %v", err)}
	}
	if task.Preview == nil || !previewEnvIDPattern.MatchString(task.Preview.EnvID) {
		return execResult{status: domain.RunnerTaskFailed, exitCode: 1, errMsg: "preview task needs a valid preview.envId"}
	}
	defer d.lockPreview(task.Preview.EnvID)()
	result, err := run(*task.Preview)
	body, _ := json.Marshal(result)
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, exitCode: 1, result: string(body), errMsg: err.Error()}
	}
	return execResult{status: domain.RunnerTaskSuccess, result: string(body)}
}

func (d *Daemon) previewDeploy(ctx context.Context, inv *domain.RunnerInvocation, spec domain.RunnerPreviewSpec) (domain.RunnerPreviewResult, error) {
	envID, started := spec.EnvID, time.Now()
	base := d.previewDir(envID)
	statePath := filepath.Join(base, "state.json")
	var state previewState
	if raw, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	if spec.SHA != "" && state.Status == domain.PreviewEnvReady && state.SHA == spec.SHA && state.Host == spec.Host {
		if _, err := os.Stat(d.previewRoutePath(envID)); err == nil {
			return domain.RunnerPreviewResult{Status: domain.PreviewEnvReady, SHA: state.SHA}, nil
		}
	}
	failed := func(sha, logs string, err error) (domain.RunnerPreviewResult, error) {
		writePreviewState(statePath, previewState{Status: domain.PreviewEnvFailed, SHA: sha, Host: spec.Host})
		if logs != "" {
			err = fmt.Errorf("%w\n--- docker compose logs --tail %s ---\n%s", err, previewLogsTail, logs)
		}
		return domain.RunnerPreviewResult{Status: domain.PreviewEnvFailed, SHA: sha, Seconds: int(time.Since(started).Seconds()), LogsTail: logs}, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return failed(spec.SHA, "", err)
	}
	src := filepath.Join(base, "src")
	sha, err := checkoutPreviewSource(ctx, src, inv, spec)
	if err != nil {
		return failed(spec.SHA, "", fmt.Errorf("checkout: %w", err))
	}
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return failed(sha, "", err)
	}
	within := previewWithinResolved(root)
	composeRel := spec.ComposeFileOrDefault()
	composeFile := filepath.Join(root, filepath.FromSlash(composeRel))
	if filepath.IsAbs(composeRel) || !within(composeFile) {
		return failed(sha, "", fmt.Errorf("compose file %q is outside the repository", composeRel))
	}
	if _, err := os.Stat(composeFile); err != nil {
		return failed(sha, "", fmt.Errorf("compose file %s not found at %s", composeRel, sha))
	}
	env := previewComposeEnv(envID, spec.Env, os.Environ())
	project := previewProject(envID)
	out, err := previewDockerStdout(ctx, env, "compose", "-p", project, "-f", composeFile, "config", "--format", "json", "--no-normalize")
	if err != nil {
		return failed(sha, "", fmt.Errorf("docker compose config: %w", err))
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		return failed(sha, "", fmt.Errorf("parse docker compose config: %w", err))
	}
	target, problems := validatePreviewCompose(cfg, within)
	if len(problems) > 0 {
		return failed(sha, "", fmt.Errorf("compose preview validation failed:\n  - %s", strings.Join(problems, "\n  - ")))
	}
	override, err := previewComposeOverride(envID, target)
	if err != nil {
		return failed(sha, "", err)
	}
	overrideFile := filepath.Join(base, "override.yaml")
	if err := os.WriteFile(overrideFile, override, 0o644); err != nil {
		return failed(sha, "", err)
	}
	if err := d.ensurePreviewEdge(ctx, envID); err != nil {
		return failed(sha, "", err)
	}
	upOut, upErr := previewDocker(ctx, env, "compose", "-p", project, "-f", composeFile, "-f", overrideFile,
		"up", "-d", "--build", "--remove-orphans", "--wait", "--wait-timeout", previewWaitTimeoutSeconds)
	logs, _ := previewDocker(ctx, env, "compose", "-p", project, "logs", "--no-color", "--tail", previewLogsTail)
	logs = tailString(strings.TrimSpace(logs), previewLogsLimit)
	if upErr != nil {
		return failed(sha, logs, fmt.Errorf("docker compose up: %v\n%s", upErr, tailString(upOut, previewOutputLimit)))
	}
	if err := d.writePreviewRoute(ctx, spec.Host, envID, target.Port); err != nil {
		return failed(sha, logs, err)
	}
	writePreviewState(statePath, previewState{Status: domain.PreviewEnvReady, SHA: sha, Host: spec.Host, Port: target.Port})
	return domain.RunnerPreviewResult{Status: domain.PreviewEnvReady, SHA: sha, Seconds: int(time.Since(started).Seconds()), LogsTail: logs}, nil
}

// checkoutPreviewSource clones the task's repository into src once and then
// fetches and checks out the requested commit (the branch head without one).
func checkoutPreviewSource(ctx context.Context, src string, inv *domain.RunnerInvocation, spec domain.RunnerPreviewSpec) (string, error) {
	if inv == nil || len(inv.Repositories) != 1 {
		return "", errors.New("preview_deploy needs exactly one repository")
	}
	repo := inv.Repositories[0]
	ctx = gitwork.WithSSHKeys(ctx, inv.GitSSHKeys)
	ctx = invocationGitHTTPContext(ctx, inv)
	ref := strings.TrimSpace(spec.Ref)
	if ref == "" {
		ref = strings.TrimSpace(repo.Ref)
	}
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		if err := os.RemoveAll(src); err != nil {
			return "", err
		}
		if err := os.MkdirAll(src, 0o755); err != nil {
			return "", err
		}
		clone := repo
		clone.Ref, clone.WorkBranch = ref, ""
		clone.RequireExistingWorkBranch, clone.CreateNewWorkBranch = false, false
		if err := checkoutRepository(ctx, src, &clone); err != nil {
			return "", err
		}
	}
	repoCtx := repositoryGitContext(ctx, repo)
	target := strings.TrimSpace(spec.SHA)
	if _, ok := gitwork.RevParse(repoCtx, src, target); target == "" || !ok {
		fetched := false
		if target != "" {
			_, err := gitwork.C(repoCtx, src, "fetch", "--quiet", "origin", target)
			fetched = err == nil
		}
		if !fetched && ref != "" {
			if _, err := gitwork.C(repoCtx, src, "fetch", "--quiet", "origin", ref); err != nil {
				return "", fmt.Errorf("fetch %s: %w", ref, err)
			}
		}
		if target == "" {
			target = "FETCH_HEAD"
		}
	}
	if _, err := gitwork.C(repoCtx, src, "checkout", "--quiet", "--force", "--detach", target); err != nil {
		return "", fmt.Errorf("checkout %s: %w", target, err)
	}
	if _, err := gitwork.C(repoCtx, src, "clean", "-ffdx", "--quiet"); err != nil {
		return "", fmt.Errorf("clean checkout: %w", err)
	}
	head, ok := gitwork.RevParse(repoCtx, src, "HEAD")
	if !ok {
		return "", errors.New("checkout has no HEAD")
	}
	if spec.SHA != "" && !strings.HasPrefix(head, strings.ToLower(strings.TrimSpace(spec.SHA))) {
		return "", fmt.Errorf("checked out %s, expected %s", head, spec.SHA)
	}
	return head, nil
}

// ensurePreviewEdge creates the preview's own internal network and attaches
// Caddy, so previews cannot reach each other through a shared edge network.
func (d *Daemon) ensurePreviewEdge(ctx context.Context, envID string) error {
	network := previewEdge(envID)
	if _, err := previewDocker(ctx, nil, "network", "inspect", network); err != nil {
		if out, err := previewDocker(ctx, nil, "network", "create", "--internal", "--label", previewLabel+"="+envID, network); err != nil {
			return fmt.Errorf("create network %s: %v (%s)", network, err, strings.TrimSpace(out))
		}
	}
	attached, err := previewDocker(ctx, nil, "inspect", d.cfg.PreviewCaddy, "-f", "{{json .NetworkSettings.Networks}}")
	if err != nil {
		return fmt.Errorf("inspect Caddy container %s: %v (%s)", d.cfg.PreviewCaddy, err, strings.TrimSpace(attached))
	}
	if !strings.Contains(attached, `"`+network+`"`) {
		if out, err := previewDocker(ctx, nil, "network", "connect", network, d.cfg.PreviewCaddy); err != nil {
			return fmt.Errorf("connect %s to %s: %v (%s)", d.cfg.PreviewCaddy, network, err, strings.TrimSpace(out))
		}
	}
	return nil
}

// writePreviewRoute writes the Caddy site and reloads; a route Caddy rejects
// is removed again so it cannot break later reloads.
func (d *Daemon) writePreviewRoute(ctx context.Context, host, envID string, port int) error {
	path := d.previewRoutePath(envID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(previewCaddyRoute(host, d.cfg.PreviewAuthUpstream, envID, port)), 0o644); err != nil {
		return err
	}
	if err := d.reloadPreviewCaddy(ctx); err != nil {
		_ = os.Remove(path)
		_ = d.reloadPreviewCaddy(ctx)
		return err
	}
	return nil
}

func (d *Daemon) reloadPreviewCaddy(ctx context.Context) error {
	out, err := previewDocker(ctx, nil, "exec", d.cfg.PreviewCaddy, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
	if err != nil {
		return fmt.Errorf("caddy reload: %v (%s)", err, tailString(out, previewOutputLimit))
	}
	return nil
}

// previewTeardown removes everything a deploy created. A missing environment
// is a success; the result counts what is still labelled for it.
func (d *Daemon) previewTeardown(ctx context.Context, envID string) (domain.RunnerPreviewResult, error) {
	project := previewProject(envID)
	env := previewComposeEnv(envID, nil, os.Environ())
	if out, err := previewDocker(ctx, env, "compose", "-p", project, "down", "-v", "--remove-orphans", "--rmi", "local"); err != nil {
		return domain.RunnerPreviewResult{Status: domain.PreviewEnvFailed}, fmt.Errorf("docker compose down: %v\n%s", err, tailString(out, previewOutputLimit))
	}
	var notes []string
	// --rmi local keeps built images with a custom tag (image: x:${LF_PREVIEW_ID}).
	if ids, err := previewImageIDs(ctx, envID); err != nil {
		notes = append(notes, err.Error())
	} else if len(ids) > 0 {
		if out, err := previewDocker(ctx, nil, append([]string{"image", "rm", "-f"}, ids...)...); err != nil {
			notes = append(notes, fmt.Sprintf("image rm: %s", tailString(strings.TrimSpace(out), previewOutputLimit)))
		}
	}
	if err := os.Remove(d.previewRoutePath(envID)); err == nil {
		if err := d.reloadPreviewCaddy(ctx); err != nil {
			// The route is gone from disk; the next reload drops it.
			notes = append(notes, err.Error())
		}
	} else if !os.IsNotExist(err) {
		return domain.RunnerPreviewResult{Status: domain.PreviewEnvFailed}, err
	}
	network := previewEdge(envID)
	_, _ = previewDocker(ctx, nil, "network", "disconnect", "-f", network, d.cfg.PreviewCaddy)
	if out, err := previewDocker(ctx, nil, "network", "rm", network); err != nil && !strings.Contains(strings.ToLower(out), "not found") {
		notes = append(notes, fmt.Sprintf("network rm %s: %s", network, strings.TrimSpace(out)))
	}
	if err := os.RemoveAll(d.previewDir(envID)); err != nil {
		return domain.RunnerPreviewResult{Status: domain.PreviewEnvFailed}, err
	}
	leftover, err := previewLeftover(ctx, envID)
	if err != nil {
		return domain.RunnerPreviewResult{Status: domain.PreviewEnvFailed}, err
	}
	if len(notes) > 0 {
		log.Printf("preview %s teardown: %s", envID, strings.Join(notes, "; "))
	}
	return domain.RunnerPreviewResult{Status: domain.PreviewEnvDestroyed, Leftover: &leftover, LogsTail: strings.Join(notes, "\n")}, nil
}

// previewLeftover counts resources labelled lf.preview=<envId> or belonging
// to the compose project.
func previewLeftover(ctx context.Context, envID string) (domain.RunnerPreviewLeftover, error) {
	filters := []string{"label=" + previewLabel + "=" + envID, "label=com.docker.compose.project=" + previewProject(envID)}
	count := func(kind ...string) (int, error) {
		ids := map[string]bool{}
		for _, filter := range filters {
			out, err := previewDocker(ctx, nil, append(kind, "--filter", filter, "-q")...)
			if err != nil {
				return 0, fmt.Errorf("docker %s: %v (%s)", strings.Join(kind, " "), err, strings.TrimSpace(out))
			}
			for _, id := range strings.Fields(out) {
				ids[id] = true
			}
		}
		return len(ids), nil
	}
	var left domain.RunnerPreviewLeftover
	var err error
	if left.Containers, err = count("ps", "-a"); err != nil {
		return left, err
	}
	if left.Volumes, err = count("volume", "ls"); err != nil {
		return left, err
	}
	if left.Networks, err = count("network", "ls"); err != nil {
		return left, err
	}
	ids, err := previewImageIDs(ctx, envID)
	left.Images = len(ids)
	return left, err
}

// previewImageIDs lists images built for the preview, which the override
// labels with lf.preview and the compose project.
func previewImageIDs(ctx context.Context, envID string) ([]string, error) {
	ids := []string{}
	for _, filter := range []string{"label=" + previewLabel + "=" + envID, "label=com.docker.compose.project=" + previewProject(envID)} {
		out, err := previewDocker(ctx, nil, "image", "ls", "-q", "--no-trunc", "--filter", filter)
		if err != nil {
			return nil, fmt.Errorf("docker image ls: %v (%s)", err, strings.TrimSpace(out))
		}
		for _, id := range strings.Fields(out) {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// previewComposeEnv is the whole environment of compose processes: what the
// docker CLI needs plus the injected interpolation variables. Nothing else
// from the runner (its tokens and LOOPFORGE_PREVIEW_SECRET) is visible to
// repository-controlled ${VAR} interpolation.
func previewComposeEnv(envID string, extra map[string]string, host []string) []string {
	keep := []string{"PATH", "HOME", "USER", "TMPDIR", "XDG_RUNTIME_DIR", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY"}
	env := []string{}
	for _, kv := range host {
		if key, _, _ := strings.Cut(kv, "="); slices.Contains(keep, key) {
			env = append(env, kv)
		}
	}
	env = append(env, "DOCKER_BUILDKIT=1")
	keys := make([]string, 0, len(extra))
	for key := range extra {
		if key != "" && !strings.ContainsAny(key, "=\x00") && key != "LF_PREVIEW_ID" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		env = append(env, key+"="+extra[key])
	}
	return append(env, "LF_PREVIEW_ID="+envID)
}

// previewWithinResolved is previewPathWithin with symlinks resolved, so a
// repository symlink cannot point compose at a host file. root must already
// be resolved. A path that does not exist yet is judged lexically; compose
// fails on it by itself.
func previewWithinResolved(root string) func(string) bool {
	lexical := previewPathWithin(root)
	return func(path string) bool {
		if !lexical(path) {
			return false
		}
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return lexical(real)
		}
		return true
	}
}

func writePreviewState(path string, state previewState) {
	body, _ := json.Marshal(state)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		log.Printf("preview state %s: %v", path, err)
	}
}

// previewDocker runs the docker CLI with an argument vector (never a shell)
// and returns combined output. env nil inherits the runner environment, for
// commands that interpolate nothing from the repository.
func previewDocker(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// previewDockerStdout keeps stdout clean for JSON; stderr goes into the error.
func previewDockerStdout(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, tailString(stderr.String(), previewOutputLimit))
	}
	return string(out), nil
}

func firstPreviewSetting(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}
