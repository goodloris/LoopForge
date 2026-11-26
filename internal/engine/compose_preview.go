package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/previewenv"
	"agentflow/internal/runtime"
)

// Compose preview environments, control-plane side
// (docs/proposals/compose-preview-environments.md §7): the synthetic
// af-preview-deploy node deploys every changed repository of a run to the
// dedicated preview runner, and a sweep tears environments down once their
// requirement is finished.

const (
	composePreviewDeployTimeout   = 30 * time.Minute
	composePreviewTeardownTimeout = 10 * time.Minute
	// A teardown still destroying after this long is assumed lost and retried.
	composePreviewTeardownStale = 15 * time.Minute
	composePreviewSweepInterval = time.Minute
	// PreviewAdminUser is the application account the injected password belongs to.
	PreviewAdminUser = "preview"
)

// ComposePreviewConfig is read from AGENTFLOW_COMPOSE_PREVIEW_*. The feature
// is off unless all three values are present and valid.
type ComposePreviewConfig struct {
	RunnerID string
	BaseURL  string // contains {id}, e.g. http://{id}.10-0-0-1.sslip.io:8088
	Secret   []byte
}

func LoadComposePreviewConfig() ComposePreviewConfig {
	return ComposePreviewConfig{
		RunnerID: strings.TrimSpace(os.Getenv("AGENTFLOW_COMPOSE_PREVIEW_RUNNER_ID")),
		BaseURL:  strings.TrimSpace(os.Getenv("AGENTFLOW_COMPOSE_PREVIEW_BASE_URL")),
		Secret:   []byte(strings.TrimSpace(os.Getenv("AGENTFLOW_COMPOSE_PREVIEW_SECRET"))),
	}
}

// Problem explains why compose previews are disabled; empty means enabled.
func (c ComposePreviewConfig) Problem() string {
	var missing []string
	if c.RunnerID == "" {
		missing = append(missing, "AGENTFLOW_COMPOSE_PREVIEW_RUNNER_ID")
	}
	if c.BaseURL == "" {
		missing = append(missing, "AGENTFLOW_COMPOSE_PREVIEW_BASE_URL")
	}
	if len(c.Secret) == 0 {
		missing = append(missing, "AGENTFLOW_COMPOSE_PREVIEW_SECRET")
	}
	if len(missing) > 0 {
		return "Compose 预览环境未启用：缺少 " + strings.Join(missing, "、")
	}
	if !strings.Contains(c.BaseURL, "{id}") {
		return "Compose 预览环境未启用：AGENTFLOW_COMPOSE_PREVIEW_BASE_URL 必须包含 {id}"
	}
	if u, err := url.Parse(c.URL("pe-0000000000")); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "Compose 预览环境未启用：AGENTFLOW_COMPOSE_PREVIEW_BASE_URL 不是 http(s) 地址"
	}
	if len(c.Secret) < previewenv.MinSecretLen {
		return fmt.Sprintf("Compose 预览环境未启用：AGENTFLOW_COMPOSE_PREVIEW_SECRET 至少 %d 字节", previewenv.MinSecretLen)
	}
	return ""
}

// URL is the preview origin of envID, without a trailing slash.
func (c ComposePreviewConfig) URL(envID string) string {
	return strings.TrimRight(strings.ReplaceAll(c.BaseURL, "{id}", envID), "/")
}

// Host is the URL host without port, used for the runner's Caddy route.
func (c ComposePreviewConfig) Host(envID string) string {
	u, err := url.Parse(c.URL(envID))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// TicketURL mints a one-time ticket and returns the URL that opens envID.
func (c ComposePreviewConfig) TicketURL(envID string, now time.Time) (string, error) {
	if problem := c.Problem(); problem != "" {
		return "", errors.New(problem)
	}
	ticket, err := previewenv.MintTicket(c.Secret, envID, now)
	if err != nil {
		return "", err
	}
	return c.URL(envID) + "/?" + previewenv.TicketParam + "=" + url.QueryEscape(ticket), nil
}

func (e *Engine) SetComposePreview(cfg ComposePreviewConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.composePreview = cfg
}

func (e *Engine) ComposePreview() ComposePreviewConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.composePreview
}

// composePreviewRunner returns the configured preview runner or why it
// cannot take a compose preview task.
func (e *Engine) composePreviewRunner(cfg ComposePreviewConfig) (domain.Runner, error) {
	if problem := cfg.Problem(); problem != "" {
		return domain.Runner{}, errors.New(problem)
	}
	runner, ok := e.repo.Runner(cfg.RunnerID)
	if !ok {
		return domain.Runner{}, fmt.Errorf("预览执行机器 %s 未注册", cfg.RunnerID)
	}
	if reason := runner.ComposePreviewUnsupportedReason(); reason != "" {
		return domain.Runner{}, errors.New(reason)
	}
	return runner, nil
}

// executeComposePreviewNode runs the af-preview-deploy node: push every
// changed work branch, then deploy each repository that has a compose file.
func (e *Engine) executeComposePreviewNode(ctx context.Context, runID string, node domain.WorkflowNode, attempt, generation int) {
	fail := func(err error) {
		if ctx.Err() == nil {
			e.nodeFailedAt(runID, node, attempt, generation, err, false)
		}
	}
	run, ok := e.repo.RunState(runID)
	if !ok {
		return
	}
	cfg := e.ComposePreview()
	runner, err := e.composePreviewRunner(cfg)
	if err != nil {
		fail(err)
		return
	}
	var heads []domain.RunnerWorkBranchHead
	if len(composePreviewRepositories(run)) > 0 {
		identity := resolveGitIdentity(e.repo, run.ExecutionContext.ResponsibleUserID)
		heads, err = e.PushConversationWorkspace(ctx, runID, run.ExecutionContext.ResponsibleUserID, identity.name, identity.email, "Loopool: 部署预览环境", "")
		if err != nil {
			fail(fmt.Errorf("推送工作分支失败：%w", err))
			return
		}
	}
	output, err := e.deployComposePreviews(ctx, run, node.ID, cfg, runner, heads)
	if err != nil {
		fail(err)
		return
	}
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if !shouldCommitNodeOutcome(*r, nr, generation, attempt, true) {
			return
		}
		nr.Output = output
		nr.Error = ""
		nr.Status = "succeeded"
		nr.FinishedAt = time.Now().UTC()
		e.event(r, node.ID, "node.succeeded", node.Name+" 已完成")
	})
}

func composePreviewRepositories(run domain.Run) []domain.RepositoryContext {
	var out []domain.RepositoryContext
	for _, rc := range run.RepositoryContexts() {
		if writableWorkBranch(rc) != "" && strings.TrimSpace(rc.CloneURL) != "" {
			out = append(out, rc)
		}
	}
	return out
}

// deployComposePreviews deploys each pushed repository whose commit carries
// the compose file and returns the node output. Every repository is tried;
// any failure fails the node after the others were attempted.
// ponytail: repositories deploy one after another; run them in parallel if
// multi-repository previews become common.
func (e *Engine) deployComposePreviews(ctx context.Context, run domain.Run, nodeID string, cfg ComposePreviewConfig, runner domain.Runner, heads []domain.RunnerWorkBranchHead) (string, error) {
	composeFile := run.WorkflowSnapshot.Preview.ComposeFileOrDefault()
	headByRepo := map[string]domain.RunnerWorkBranchHead{}
	for _, head := range heads {
		headByRepo[head.RepositoryID] = head
	}
	skip := func(lines *[]string, rc domain.RepositoryContext, reason string) {
		*lines = append(*lines, fmt.Sprintf("- %s：已跳过，%s", rc.Name, reason))
		e.observe(run.ID, nodeID, domain.TelemetryEvent{Category: "preview", Name: "preview.env.skipped", Phase: "finish", Message: rc.Name + "：" + reason, Attributes: map[string]any{"repositoryId": rc.RepositoryID}})
	}
	var lines, failures []string
	for _, rc := range composePreviewRepositories(run) {
		head, ok := headByRepo[rc.RepositoryID]
		if !ok || strings.TrimSpace(head.Head) == "" {
			skip(&lines, rc, "工作分支没有改动")
			continue
		}
		found, err := e.composeFileAt(ctx, run, rc, composeFile)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s：读取 %s 失败：%v", rc.Name, composeFile, err))
			continue
		}
		if !found {
			skip(&lines, rc, "提交 "+shortSHA(head.Head)+" 中没有 "+composeFile)
			continue
		}
		env, err := e.deployComposePreview(ctx, run, nodeID, cfg, runner, rc, head.Head, composeFile)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s：%v", rc.Name, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s：%s（%s，管理员 %s / %s）", rc.Name, env.URL, shortSHA(env.SHA), PreviewAdminUser, env.AdminPassword))
	}
	if len(failures) > 0 {
		return "", fmt.Errorf("Compose 预览部署失败：%s", strings.Join(failures, "；"))
	}
	if len(lines) == 0 {
		lines = append(lines, "- 没有可部署的代码仓库")
	}
	return "# 预览环境\n\n" + strings.Join(lines, "\n") + "\n", nil
}

// deployComposePreview upserts one environment as deploying, dispatches
// preview_deploy and records the outcome. The env id, URL and admin password
// survive reruns of the same requirement and repository.
func (e *Engine) deployComposePreview(ctx context.Context, run domain.Run, nodeID string, cfg ComposePreviewConfig, runner domain.Runner, rc domain.RepositoryContext, sha, composeFile string) (domain.PreviewEnvironment, error) {
	owner := strings.TrimSpace(run.ExecutionContext.IssueID)
	if owner == "" {
		owner = run.ID
	}
	envID := domain.PreviewEnvID(owner, rc.RepositoryID)
	env, found := e.repo.PreviewEnvironment(envID)
	if !found || env.AdminPassword == "" {
		password, err := randomPreviewPassword()
		if err != nil {
			return env, err
		}
		env.AdminPassword = password
	}
	env.ID = envID
	env.ProjectID = run.ExecutionContext.ProjectID
	env.IssueID = strings.TrimSpace(run.ExecutionContext.IssueID)
	env.RepositoryID = rc.RepositoryID
	env.RunnerID = runner.ID
	env.LastRunID = run.ID
	env.Branch = writableWorkBranch(rc)
	env.URL = cfg.URL(envID)
	env.Status = domain.PreviewEnvDeploying
	env.LastError = ""
	env.DestroyedAt = time.Time{}
	if err := e.repo.PutPreviewEnvironment(env); err != nil {
		return env, err
	}
	e.observe(run.ID, nodeID, domain.TelemetryEvent{Category: "preview", Name: "preview.env.deploying", Phase: "start", Message: rc.Name + " → " + env.URL, Attributes: map[string]any{"envId": envID, "sha": sha, "runnerId": runner.ID}})
	userID := run.ExecutionContext.ResponsibleUserID
	useSSH := rc.Provider == domain.RepositoryProviderSSH || gitwork.IsSSHReference(rc.CloneURL)
	task := domain.RunnerTask{
		ID:          fmt.Sprintf("rt-%d-%s-deploy", time.Now().UnixNano(), envID),
		RunnerID:    runner.ID,
		Provider:    "terminal",
		Action:      domain.RunnerTaskActionPreviewDeploy,
		Workspace:   "preview/" + envID,
		Status:      domain.RunnerTaskQueued,
		MaxAttempts: 1,
		Invocation: &domain.RunnerInvocation{GitHTTPUserID: userID, Repositories: []domain.RunnerRepository{{
			RepositoryID: rc.RepositoryID, Name: rc.Name, CloneURL: rc.CloneURL, Ref: env.Branch, WorkBranch: env.Branch, UseSSH: useSSH,
		}}},
		Preview: &domain.RunnerPreviewSpec{
			EnvID:       envID,
			ComposeFile: composeFile,
			Host:        cfg.Host(envID),
			Env:         map[string]string{"LF_PREVIEW_ID": envID, "PREVIEW_ADMIN_PASSWORD": env.AdminPassword},
			Ref:         env.Branch,
			SHA:         sha,
		},
	}
	if useSSH {
		task.Invocation.GitSSHUserID = userID
	}
	final, err := e.runPreviewTask(ctx, task, composePreviewDeployTimeout)
	if err == nil {
		var result domain.RunnerPreviewResult
		if decodeErr := json.Unmarshal([]byte(final.Result), &result); decodeErr == nil && strings.TrimSpace(result.SHA) != "" {
			sha = strings.TrimSpace(result.SHA)
		}
		env.Status, env.SHA, env.DeployedAt = domain.PreviewEnvReady, sha, time.Now().UTC()
	} else {
		env.Status, env.LastError = domain.PreviewEnvFailed, err.Error()
	}
	// The environment row must reflect the outcome even when the node context
	// was cancelled while waiting.
	if putErr := e.repo.PutPreviewEnvironment(env); putErr != nil && err == nil {
		err = putErr
	}
	e.observe(run.ID, nodeID, domain.TelemetryEvent{Category: "preview", Name: "preview.env." + env.Status, Phase: "finish", Message: rc.Name + " → " + env.URL, Attributes: map[string]any{"envId": envID, "sha": env.SHA}})
	return env, err
}

// runPreviewTask queues a preview runner task and waits for it; a task that
// did not succeed is returned as an error carrying the runner's message.
func (e *Engine) runPreviewTask(ctx context.Context, task domain.RunnerTask, timeout time.Duration) (domain.RunnerTask, error) {
	if err := e.repo.PutRunnerTask(task); err != nil {
		return task, err
	}
	e.notifyRunnerDispatch(task.Provider)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	final, err := e.awaitRunnerTask(ctx, task.ID, nil)
	if err != nil {
		return final, fmt.Errorf("%s 未完成：%w", task.Action, err)
	}
	if final.Status != domain.RunnerTaskSuccess {
		msg := strings.TrimSpace(final.Error)
		if msg == "" {
			msg = task.Action + " " + final.Status
		}
		return final, errors.New(msg)
	}
	return final, nil
}

// composeFileAt reports whether the pushed work branch of rc carries file.
// It fetches only the branch tip's trees into a scratch repository, so it
// works whether the run's checkout lives here or on an execution machine.
func (e *Engine) composeFileAt(ctx context.Context, run domain.Run, rc domain.RepositoryContext, file string) (bool, error) {
	userID := run.ExecutionContext.ResponsibleUserID
	ctx = withRunGitCredentials(ctx, runtime.RunCredentials{GitHTTPCredentials: e.repo.GitHTTPCredentialSecrets(ctx, userID)})
	ctx = gitwork.WithSSHKeys(e.withSSHKnownHosts(ctx), e.repo.GitSSHKeysForRemotes(userID, sshRepositoryURLs([]domain.RepositoryContext{rc})))
	ctx = withRepositoryGit(ctx, rc)
	dir, err := os.MkdirTemp("", "lf-compose-check-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	if _, err := gitwork.C(ctx, dir, "init", "-q"); err != nil {
		return false, err
	}
	if _, err := gitwork.C(ctx, dir, "fetch", "-q", "--depth=1", "--filter=blob:none", rc.CloneURL, "refs/heads/"+writableWorkBranch(rc)); err != nil {
		return false, explainGitAuthFailure(err, true)
	}
	out, err := gitwork.C(ctx, dir, "ls-tree", "--name-only", "FETCH_HEAD", "--", file)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func randomPreviewPassword() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func shortSHA(sha string) string {
	if sha = strings.TrimSpace(sha); len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// StartPreviewTeardown claims envID for teardown and tears it down in the
// background. It returns the environment as stored after the claim and
// whether this call started the teardown; deploying environments and fresh
// teardowns are not claimed.
func (e *Engine) StartPreviewTeardown(envID string) (domain.PreviewEnvironment, bool, error) {
	env, claimed, err := e.repo.ClaimPreviewEnvironmentTeardown(envID, time.Now().Add(-composePreviewTeardownStale))
	if err != nil || !claimed {
		current, _ := e.repo.PreviewEnvironment(envID)
		return current, false, err
	}
	go e.teardownPreviewEnvironment(context.Background(), env)
	return env, true, nil
}

// teardownPreviewEnvironment dispatches preview_teardown for a claimed
// (destroying) environment. Failure leaves it destroying with last_error, so
// the sweep retries it once the claim goes stale.
func (e *Engine) teardownPreviewEnvironment(ctx context.Context, env domain.PreviewEnvironment) {
	runnerID := env.RunnerID
	if runnerID == "" {
		runnerID = e.ComposePreview().RunnerID
	}
	runner, ok := e.repo.Runner(runnerID)
	var err error
	switch {
	case !ok:
		err = fmt.Errorf("预览执行机器 %s 未注册", runnerID)
	case runner.ComposePreviewUnsupportedReason() != "":
		err = errors.New(runner.ComposePreviewUnsupportedReason())
	default:
		_, err = e.runPreviewTask(ctx, domain.RunnerTask{
			ID:          fmt.Sprintf("rt-%d-%s-teardown", time.Now().UnixNano(), env.ID),
			RunnerID:    runner.ID,
			Provider:    "terminal",
			Action:      domain.RunnerTaskActionPreviewTeardown,
			Workspace:   "preview/" + env.ID,
			Status:      domain.RunnerTaskQueued,
			MaxAttempts: 1,
			Preview:     &domain.RunnerPreviewSpec{EnvID: env.ID},
		}, composePreviewTeardownTimeout)
	}
	if err != nil {
		env.LastError = "销毁失败：" + err.Error()
		log.Printf("compose preview teardown %s: %v", env.ID, err)
	} else {
		env.Status, env.LastError, env.DestroyedAt = domain.PreviewEnvDestroyed, "", time.Now().UTC()
	}
	if putErr := e.repo.PutPreviewEnvironment(env); putErr != nil {
		log.Printf("compose preview teardown %s: %v", env.ID, putErr)
	}
}

// previewTeardownDue reports whether env's requirement has ended: the
// requirement is done/cancelled/closed or gone, or its delivery MRs were all
// merged or closed. Environments without a requirement are only destroyed by
// hand, and an environment whose last run is still active is left alone (a
// rerun of a reopened requirement may still carry the old MR state).
func (e *Engine) previewTeardownDue(env domain.PreviewEnvironment) bool {
	if env.IssueID == "" {
		return false
	}
	switch env.Status {
	case domain.PreviewEnvReady, domain.PreviewEnvFailed, domain.PreviewEnvDestroying:
	default:
		return false
	}
	if env.LastRunID != "" {
		if run, err := e.repo.RunHeader(env.LastRunID); err == nil && !domain.TerminalRunStatus(run.Status) {
			return false
		}
	}
	issue, ok := e.repo.Issue(env.IssueID)
	if !ok {
		return true
	}
	switch issue.Status {
	case "done", "cancelled", "closed":
		return true
	}
	switch domain.AggregateDeliveryMRState(issue.DeliveryMRList()) {
	case "merged", "integrated", "closed":
		return true
	}
	return false
}

// SweepPreviewTeardowns is the single place that reacts to a requirement
// ending, whichever path changed it (MR sync, tracker sync, manual edit):
// it claims every due environment and tears it down in the background.
func (e *Engine) SweepPreviewTeardowns() int {
	started := 0
	for _, env := range e.repo.LivePreviewEnvironments() {
		if !e.previewTeardownDue(env) {
			continue
		}
		if _, ok, err := e.StartPreviewTeardown(env.ID); err != nil {
			log.Printf("compose preview sweep %s: %v", env.ID, err)
		} else if ok {
			started++
		}
	}
	return started
}

func (e *Engine) previewTeardownLoop(ctx context.Context) {
	ticker := time.NewTicker(composePreviewSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e.ComposePreview().Problem() == "" {
				e.SweepPreviewTeardowns()
			}
		}
	}
}
