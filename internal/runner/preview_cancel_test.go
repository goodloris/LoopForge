package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// TestPreviewDeployCancelKillsCompose puts a fake docker on PATH whose
// `compose up` blocks in a child process that ignores SIGTERM, cancels the
// deploy and checks that previewDeploy returns promptly and that neither the
// fake CLI nor its child survives.
func TestPreviewDeployCancelKillsCompose(t *testing.T) {
	fake := t.TempDir()
	envID := "pe-cancel1"
	writeFile(t, filepath.Join(fake, "config.json"), `{"x-loopforge-preview":{"service":"web","port":80},"services":{"web":{"image":"nginx"}}}`)
	script := `#!/bin/sh
case " $* " in
  *" config "*) cat "` + fake + `/config.json" ;;
  *" up "*)
    trap '' TERM
    sh -c "trap '' TERM; sleep 300" &
    echo $! > "` + fake + `/child.pid"
    echo $$ > "` + fake + `/leader.pid"
    wait ;;
  " inspect "*) echo '{"lf-edge-` + envID + `":{}}' ;;
  *) exit 0 ;;
esac
`
	writeFile(t, filepath.Join(fake, "docker"), script)
	if err := os.Chmod(filepath.Join(fake, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := filepath.Join(t.TempDir(), "repo")
	writeFile(t, filepath.Join(repo, domain.PreviewDefaultComposeFile), "services: {}\n")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	d := &Daemon{cfg: Config{PreviewRoot: t.TempDir(), PreviewCaddy: "lf-caddy", PreviewAuthUpstream: "host.docker.internal:9001"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result domain.RunnerPreviewResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: repo, Ref: "main"}}}
		result, err := d.previewDeploy(ctx, inv, domain.RunnerPreviewSpec{EnvID: envID, Host: envID + ".preview.test", Ref: "main"})
		done <- outcome{result, err}
	}()

	childPID := waitForPID(t, filepath.Join(fake, "child.pid"), done)
	leaderPID := waitForPID(t, filepath.Join(fake, "leader.pid"), done)
	cancel()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("previewDeploy did not return after cancel")
	}
	if got.err == nil || got.result.Status != domain.PreviewEnvFailed {
		t.Fatalf("canceled deploy = %+v %v", got.result, got.err)
	}
	for name, pid := range map[string]int{"fake docker": leaderPID, "compose child": childPID} {
		if processAlive(pid, 3*time.Second) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("%s (pid %d) survived the cancel", name, pid)
		}
	}
	if exec := previewExecResult(ctx, got.result, got.err); exec.status != domain.RunnerTaskCanceled {
		t.Fatalf("task status = %s, want %s", exec.status, domain.RunnerTaskCanceled)
	}
}

func TestPreviewExecResult(t *testing.T) {
	live := context.Background()
	if r := previewExecResult(live, domain.RunnerPreviewResult{Status: domain.PreviewEnvReady}, nil); r.status != domain.RunnerTaskSuccess {
		t.Fatalf("success: %+v", r)
	}
	if r := previewExecResult(live, domain.RunnerPreviewResult{}, errors.New("boom")); r.status != domain.RunnerTaskFailed || r.errMsg != "boom" {
		t.Fatalf("failure: %+v", r)
	}
	stopped, cancel := context.WithCancel(live)
	cancel()
	if r := previewExecResult(stopped, domain.RunnerPreviewResult{}, errors.New("signal: killed")); r.status != domain.RunnerTaskCanceled || !strings.HasPrefix(r.errMsg, "canceled") {
		t.Fatalf("canceled: %+v", r)
	}
}

func waitForPID[T any](t *testing.T, path string, done <-chan T) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return pid
			}
		}
		select {
		case v := <-done:
			t.Fatalf("previewDeploy returned before compose up started: %+v", v)
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("%s never written", path)
	return 0
}

// processAlive polls until pid is gone or wait elapses; an orphan needs a
// moment to be reaped by init.
func processAlive(pid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if syscall.Kill(pid, 0) != nil {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
}
