package runner

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The fixture is `docker compose config --format json --no-normalize` of
// merchant-hub's .loopforge/preview/compose.yaml with the checkout at /src.
func loadMerchantHubConfig(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/preview-merchant-hub.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestValidatePreviewComposeAcceptsMerchantHub(t *testing.T) {
	target, errs := validatePreviewCompose(loadMerchantHubConfig(t), previewPathWithin("/src"))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := previewComposeTarget{Service: "web", Port: 8080, Services: []string{"seed", "web"}, Volumes: []string{"data"}}
	if !reflect.DeepEqual(target, want) {
		t.Fatalf("target = %+v, want %+v", target, want)
	}
}

func TestValidatePreviewComposeRejects(t *testing.T) {
	// Each case patches a minimal valid config; want is a substring of the
	// single expected error.
	cases := []struct {
		name  string
		patch string // JSON merged into services.web, or a top-level patch when it starts with {"top":
		want  string
	}{
		{"ports", `{"ports":[{"target":80,"published":"8080"}]}`, "ports are not allowed"},
		{"privileged", `{"privileged":true}`, "privileged is not allowed"},
		{"cap_add", `{"cap_add":["SYS_ADMIN"]}`, "cap_add is not allowed"},
		{"devices", `{"devices":[{"source":"/dev/kvm","target":"/dev/kvm"}]}`, "devices is not allowed"},
		{"container_name", `{"container_name":"fixed"}`, "container_name is not allowed"},
		{"userns_mode", `{"userns_mode":"host"}`, "userns_mode is not allowed"},
		{"cgroup_parent", `{"cgroup_parent":"/x"}`, "cgroup_parent is not allowed"},
		{"security_opt", `{"security_opt":["seccomp=unconfined"]}`, "security_opt is not allowed"},
		{"network_mode host", `{"network_mode":"host"}`, "network_mode=host"},
		{"network_mode container", `{"network_mode":"container:abc"}`, "network_mode=container:abc"},
		{"network_mode bridge", `{"network_mode":"bridge"}`, "network_mode=bridge"},
		{"pid host", `{"pid":"host"}`, "pid=host"},
		{"pid container", `{"pid":"container:abc"}`, "pid=container:abc"},
		{"ipc host", `{"ipc":"host"}`, "ipc=host"},
		{"uts host", `{"uts":"host"}`, "uts=host"},
		{"cgroup host", `{"cgroup":"host"}`, "cgroup=host"},
		{"bind mount", `{"volumes":[{"type":"bind","source":"/etc","target":"/h"}]}`, "bind mount /etc"},
		{"tmpfs mount", `{"volumes":[{"type":"tmpfs","target":"/t"}]}`, "tmpfs mount"},
		{"volumes_from container", `{"volumes_from":["container:abc"]}`, "volumes_from container:abc"},
		{"env_file outside", `{"env_file":[{"path":"/home/runner/.env","required":true}]}`, "env_file /home/runner/.env"},
		{"build context outside", `{"build":{"context":"/"}}`, "build context /"},
		{"dockerfile outside", `{"build":{"context":"/src","dockerfile":"../etc/Dockerfile"}}`, "Dockerfile /etc/Dockerfile"},
		{"additional context outside", `{"build":{"context":"/src","additional_contexts":{"x":"/etc"}}}`, "additional_contexts.x"},
		{"build ssh", `{"build":{"context":"/src","ssh":["default"]}}`, "build ssh"},
		{"build network host", `{"build":{"context":"/src","network":"host"}}`, "build network=host"},
		{"external network", `{"top":{"networks":{"shared":{"external":true}}}}`, "external networks are not allowed"},
		{"external volume", `{"top":{"volumes":{"data":{"external":true}}}}`, "external volumes are not allowed"},
		{"driver_opts volume", `{"top":{"volumes":{"data":{"driver_opts":{"type":"none","o":"bind","device":"/srv"}}}}}`, "driver_opts are not allowed"},
		{"config file outside", `{"top":{"configs":{"c":{"file":"/etc/hosts"}}}}`, "configs c: file /etc/hosts"},
		{"secret file outside", `{"top":{"secrets":{"s":{"file":"/root/.ssh/id_rsa"}}}}`, "secrets s: file"},
		{"missing meta", `{"top":{"x-loopforge-preview":null}}`, "x-loopforge-preview is required"},
		{"unknown entry", `{"top":{"x-loopforge-preview":{"service":"api","port":80}}}`, `service "api" is not a service`},
		{"string port", `{"top":{"x-loopforge-preview":{"service":"web","port":"80"}}}`, "port must be an integer"},
		{"port out of range", `{"top":{"x-loopforge-preview":{"service":"web","port":70000}}}`, "port must be an integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalPreviewConfig(t)
			var patch map[string]any
			if err := json.Unmarshal([]byte(tc.patch), &patch); err != nil {
				t.Fatal(err)
			}
			if top, ok := patch["top"].(map[string]any); ok {
				for k, v := range top {
					if v == nil {
						delete(cfg, k)
					} else {
						cfg[k] = v
					}
				}
			} else {
				web := cfg["services"].(map[string]any)["web"].(map[string]any)
				for k, v := range patch {
					web[k] = v
				}
			}
			_, errs := validatePreviewCompose(cfg, previewPathWithin("/src"))
			// A missing entry also makes the port check irrelevant; the
			// expected error just has to be among them.
			if len(errs) == 0 || !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, tc.want) }) {
				t.Fatalf("errors %q do not contain %q", errs, tc.want)
			}
			if tc.name != "missing meta" && len(errs) != 1 {
				t.Fatalf("want exactly one error, got %q", errs)
			}
		})
	}
}

func TestValidatePreviewComposeAllowsInsideRepository(t *testing.T) {
	cfg := minimalPreviewConfig(t)
	web := cfg["services"].(map[string]any)["web"].(map[string]any)
	web["env_file"] = []any{map[string]any{"path": "/src/.env.preview"}}
	cfg["services"].(map[string]any)["worker"] = map[string]any{"image": "busybox", "network_mode": "none"}
	cfg["configs"] = map[string]any{"c": map[string]any{"file": "/src/conf/app.ini"}}
	if _, errs := validatePreviewCompose(cfg, previewPathWithin("/src")); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func minimalPreviewConfig(t *testing.T) map[string]any {
	t.Helper()
	var cfg map[string]any
	raw := `{
	  "x-loopforge-preview": {"service": "web", "port": 80},
	  "services": {"web": {"image": "nginx:alpine", "expose": ["80"],
	    "build": {"context": "/src", "dockerfile": "Dockerfile"},
	    "volumes": [{"type": "volume", "source": "data", "target": "/d"}]}},
	  "volumes": {"data": {}}
	}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPreviewComposeOverride(t *testing.T) {
	target := previewComposeTarget{Service: "web", Port: 8080, Services: []string{"seed", "web", "worker"}, Networks: []string{"backend"}, Volumes: []string{"data"}}
	raw, err := previewComposeOverride("pe-0123456789", target, map[string]bool{"worker": true})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(`{
	  "services": {
	    "seed": {"mem_limit": "2g", "cpus": 2, "pids_limit": 512, "security_opt": ["no-new-privileges:true"],
	      "labels": {"lf.preview": "pe-0123456789"}, "networks": {"default": {}}},
	    "web": {"mem_limit": "2g", "cpus": 2, "pids_limit": 512, "security_opt": ["no-new-privileges:true"],
	      "labels": {"lf.preview": "pe-0123456789"},
	      "networks": {"default": {}, "edge": {"aliases": ["lf-pe-0123456789"]}}},
	    "worker": {"mem_limit": "2g", "cpus": 2, "pids_limit": 512, "security_opt": ["no-new-privileges:true"],
	      "labels": {"lf.preview": "pe-0123456789"}}
	  },
	  "networks": {
	    "default": {"internal": true, "labels": {"lf.preview": "pe-0123456789"}},
	    "backend": {"internal": true, "labels": {"lf.preview": "pe-0123456789"}},
	    "edge": {"name": "lf-edge-pe-0123456789", "external": true}
	  },
	  "volumes": {"data": {"labels": {"lf.preview": "pe-0123456789"}}}
	}`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override =\n%s", raw)
	}
}

func TestPreviewCaddyRoute(t *testing.T) {
	got := previewCaddyRoute("pe-0123456789.preview.example", "host.docker.internal:9001", "pe-0123456789", 8080)
	want := "http://pe-0123456789.preview.example {\n\tforward_auth host.docker.internal:9001 {\n\t\turi /verify?env=pe-0123456789\n\t}\n\treverse_proxy lf-pe-0123456789:8080\n}\n"
	if got != want {
		t.Fatalf("route =\n%s", got)
	}
	for _, host := range []string{"a.example", "pe-1.192-0-2-10.sslip.io"} {
		if !previewHostPattern.MatchString(host) {
			t.Errorf("host %q rejected", host)
		}
	}
	for _, host := range []string{"", "a b", "a{", "a.example:8088", "a\nb"} {
		if previewHostPattern.MatchString(host) {
			t.Errorf("host %q accepted", host)
		}
	}
}

func TestPreviewComposeEnvHidesRunnerSecrets(t *testing.T) {
	host := []string{"PATH=/usr/bin", "HOME=/home/r", "DOCKER_HOST=unix:///x", "LOOPFORGE_PREVIEW_SECRET=s", "AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN=t"}
	got := previewComposeEnv("pe-1", map[string]string{"PREVIEW_ADMIN_PASSWORD": "pw", "LF_PREVIEW_ID": "spoof", "A=B": "x"}, host)
	want := []string{"PATH=/usr/bin", "HOME=/home/r", "DOCKER_HOST=unix:///x", "DOCKER_BUILDKIT=1", "PREVIEW_ADMIN_PASSWORD=pw", "LF_PREVIEW_ID=pe-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %q", got)
	}
}

func TestPreviewPathWithin(t *testing.T) {
	within := previewPathWithin("/src")
	for path, ok := range map[string]bool{"/src": true, "/src/a/b": true, "/srcx": false, "/": false, "/src/../etc": false} {
		if within(path) != ok {
			t.Errorf("within(%q) = %v", path, !ok)
		}
	}
}
