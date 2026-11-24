package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A custom image built FROM the platform image inherits /app/bin/agentflow
// as ENTRYPOINT, which would swallow the keep-alive command; such images get
// --entrypoint sh. An image with its own entrypoint (node's
// docker-entrypoint.sh) or none keeps it.
func TestSandboxKeepAliveOverridesOnlyThePlatformEntrypoint(t *testing.T) {
	runtimeImage := []string{"/app/bin/agentflow"}
	for _, tc := range []struct {
		name       string
		entrypoint []string
		platform   []string
		want       bool
	}{
		{"FROM the platform image", []string{"/app/bin/agentflow"}, runtimeImage, true},
		{"FROM a runtime image with another entrypoint", []string{"/opt/lf/agentflow", "-x"}, []string{"/opt/lf/agentflow", "-x"}, true},
		{"node image", []string{"docker-entrypoint.sh"}, runtimeImage, false},
		{"no entrypoint", nil, runtimeImage, false},
		{"ENTRYPOINT []", []string{}, runtimeImage, false},
	} {
		if got := inheritsPlatformEntrypoint(tc.entrypoint, tc.platform); got != tc.want {
			t.Errorf("%s: inheritsPlatformEntrypoint = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := sandboxKeepAliveArgs("img", true); !slices.Equal(got[:3], []string{"--entrypoint", "sh", "img"}) {
		t.Fatalf("override args = %q", got)
	}
	if got := sandboxKeepAliveArgs("img", false); got[0] != "img" || slices.Contains(got, "--entrypoint") {
		t.Fatalf("plain args = %q", got)
	}
}

// With Docker and a local platform image, a Dockerfile FROM it without
// ENTRYPOINT [] keeps running. AGENTFLOW_DOCKER_ENTRYPOINT_LIVE=<image>.
func TestSandboxFromPlatformImageStaysRunning(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("AGENTFLOW_DOCKER_ENTRYPOINT_LIVE"))
	if base == "" {
		t.Skip("set AGENTFLOW_DOCKER_ENTRYPOINT_LIVE to a platform image (e.g. loopforge-platform:local)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	image := "lf-entrypoint-test:" + strings.ToLower(time.Now().Format("150405"))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+base+"\nRUN true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "build", "-q", "-t", image, dir).CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	defer exec.Command("docker", "image", "rm", "-f", image).Run()
	entrypoint, err := dockerImageEntrypoint(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	override := inheritsPlatformEntrypoint(entrypoint, platformRuntimeEntrypoint(ctx))
	if !override {
		t.Fatalf("entrypoint %q not recognised as the platform's", entrypoint)
	}
	name := "lf-entrypoint-test-" + strings.ToLower(time.Now().Format("150405"))
	args := append([]string{"run", "-d", "--name", name}, sandboxKeepAliveArgs(image, override)...)
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("run: %v %s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", "-v", name).Run()
	time.Sleep(3 * time.Second)
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", name).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
		t.Fatalf("container is not running: %s %v; logs: %s", out, err, logs)
	}
}
