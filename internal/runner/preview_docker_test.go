package runner

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/previewenv"
)

// TestPreviewDockerLifecycle deploys and tears down a one-service compose
// preview behind a real Caddy using deploy/preview-edge/Caddyfile. It needs
// Docker and network access for nginx:alpine and caddy:2.8:
//
//	LOOPFORGE_PREVIEW_DOCKER_TEST=1 go test ./internal/runner -run TestPreviewDockerLifecycle -v
func TestPreviewDockerLifecycle(t *testing.T) {
	if os.Getenv("LOOPFORGE_PREVIEW_DOCKER_TEST") != "1" {
		t.Skip("set LOOPFORGE_PREVIEW_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	ctx := t.Context()
	envID := fmt.Sprintf("pe-it%d", time.Now().UnixNano()%1_000_000_000)
	root := t.TempDir()
	caddy := "lf-caddy-" + envID
	secret := strings.Repeat("s", previewenv.MinSecretLen)

	// Repository with a preview compose file.
	repo := filepath.Join(t.TempDir(), "repo")
	writeFile(t, filepath.Join(repo, ".loopforge/preview/compose.yaml"), `x-loopforge-preview: {service: web, port: 80}
services:
  web:
    build: {context: ../.., dockerfile: .loopforge/preview/Dockerfile}
    image: lf-preview/it:${LF_PREVIEW_ID}
    expose: ["80"]
    environment: {PREVIEW: "${LF_PREVIEW_ID}"}
    volumes: [html:/usr/share/nginx/html]
volumes:
  html: {}
`)
	writeFile(t, filepath.Join(repo, ".loopforge/preview/Dockerfile"), "FROM nginx:alpine\n")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	sha, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()

	// Verify listener reachable from Caddy through host.docker.internal.
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	auth := newPreviewAuth([]byte(secret))
	go http.Serve(listener, auth)
	defer listener.Close()
	authPort := listener.Addr().(*net.TCPAddr).Port

	routes := filepath.Join(root, "caddy", "routes")
	if err := os.MkdirAll(routes, 0o755); err != nil {
		t.Fatal(err)
	}
	caddyfile, _ := filepath.Abs("../../deploy/preview-edge/Caddyfile")
	docker(t, "run", "-d", "--name", caddy, "-p", "127.0.0.1::80", "--add-host", "host.docker.internal:host-gateway",
		"-v", caddyfile+":/etc/caddy/Caddyfile:ro", "-v", routes+":/etc/caddy/routes:ro", "caddy:2.8")
	defer exec.Command("docker", "rm", "-f", caddy).Run()
	port := strings.TrimSpace(docker(t, "port", caddy, "80/tcp"))
	port = port[strings.LastIndex(port, ":")+1:]

	d := &Daemon{cfg: Config{PreviewRoot: root, PreviewCaddy: caddy, PreviewAuthUpstream: fmt.Sprintf("host.docker.internal:%d", authPort)}}
	defer d.previewTeardown(ctx, envID)
	inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: repo, Ref: "main"}}}
	host := envID + ".preview.test"
	spec := domain.RunnerPreviewSpec{EnvID: envID, Host: host, Ref: "main", SHA: strings.TrimSpace(string(sha))}
	result, err := d.previewDeploy(ctx, inv, spec)
	if err != nil || result.Status != domain.PreviewEnvReady {
		t.Fatalf("deploy: %+v %v", result, err)
	}
	if again, err := d.previewDeploy(ctx, inv, spec); err != nil || again.Seconds != 0 || again.LogsTail != "" {
		t.Fatalf("same-SHA redeploy is not a no-op: %+v %v", again, err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, cookie string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+path, nil)
		req.Host = host
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: previewenv.CookieName, Value: cookie})
		}
		var resp *http.Response
		for i := 0; i < 20; i++ { // Caddy applies the reload asynchronously
			// 404 until Caddy applies the reload, 502/503 while the first upstream dial settles.
			if resp, err = client.Do(req); err == nil && resp.StatusCode != http.StatusNotFound &&
				resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusServiceUnavailable {
				break
			}
			if err == nil {
				resp.Body.Close()
			}
			time.Sleep(250 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}
	if resp, _ := get("/", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	ticket, _ := previewenv.MintTicket([]byte(secret), envID, time.Now())
	resp, _ := get("/?__lf="+ticket, "")
	if resp.StatusCode != http.StatusFound || len(resp.Cookies()) == 0 {
		t.Fatalf("ticket: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, body := get("/", resp.Cookies()[0].Value); resp.StatusCode != http.StatusOK || !strings.Contains(body, "nginx") {
		t.Fatalf("with cookie: %d %q", resp.StatusCode, body)
	}

	if ids, err := previewImageIDs(ctx, envID); err != nil || len(ids) != 1 {
		t.Fatalf("built image not labelled: %v %v", ids, err)
	}
	down, err := d.previewTeardown(ctx, envID)
	if err != nil || down.Status != domain.PreviewEnvDestroyed || down.Leftover == nil || *down.Leftover != (domain.RunnerPreviewLeftover{}) {
		t.Fatalf("teardown: %+v %v", down, err)
	}
	if again, err := d.previewTeardown(ctx, envID); err != nil || again.Status != domain.PreviewEnvDestroyed {
		t.Fatalf("second teardown: %+v %v", again, err)
	}
	if _, err := os.Stat(d.previewDir(envID)); !os.IsNotExist(err) {
		t.Fatalf("preview dir left: %v", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v %s", args, err, out)
	}
	return string(out)
}
