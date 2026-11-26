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
	want := previewComposeTarget{Service: "web", Port: 8080, Services: []string{"seed", "web"}, Built: []string{"seed", "web"}, Volumes: []string{"data"}}
	if !reflect.DeepEqual(target, want) {
		t.Fatalf("target = %+v, want %+v", target, want)
	}
}

func TestValidatePreviewComposeRejects(t *testing.T) {
	// Each case patches a minimal valid config; want is a substring of the
	// single expected error.
	cases := []struct {
		name  string
		patch string // JSON merged into services.web; the "top" member is merged into the top level
		want  string
	}{
		// §4 explicit rejections.
		{"ports", `{"ports":[{"target":80,"published":"8080"}]}`, "web: ports is not allowed"},
		{"privileged", `{"privileged":true}`, "web: privileged is not allowed"},
		{"cap_add", `{"cap_add":["SYS_ADMIN"]}`, "web: cap_add is not allowed"},
		{"devices", `{"devices":[{"source":"/dev/kvm","target":"/dev/kvm"}]}`, "web: devices is not allowed"},
		{"container_name", `{"container_name":"fixed"}`, "web: container_name is not allowed"},
		{"userns_mode", `{"userns_mode":"host"}`, "web: userns_mode is not allowed"},
		{"cgroup_parent", `{"cgroup_parent":"/x"}`, "web: cgroup_parent is not allowed"},
		{"network_mode host", `{"network_mode":"host"}`, "web: network_mode is not allowed"},
		{"network_mode container", `{"network_mode":"container:abc"}`, "web: network_mode is not allowed"},
		{"pid host", `{"pid":"host"}`, "web: pid is not allowed"},
		{"ipc host", `{"ipc":"host"}`, "web: ipc is not allowed"},
		{"uts host", `{"uts":"host"}`, "web: uts is not allowed"},
		{"bind mount", `{"volumes":[{"type":"bind","source":"/etc","target":"/h"}]}`, "bind mount /etc"},
		{"tmpfs mount", `{"volumes":[{"type":"tmpfs","target":"/t"}]}`, "tmpfs mount"},
		{"image mount", `{"volumes":[{"type":"image","source":"alpine","target":"/i"}]}`, "image mount alpine"},
		{"undeclared volume", `{"volumes":[{"type":"volume","source":"other","target":"/o"}]}`, "volume other is not declared"},
		{"mount bind options", `{"volumes":[{"type":"volume","source":"data","target":"/d","bind":{"propagation":"rshared"}}]}`, "volumes[0]: bind is not allowed"},
		{"external network", `{"top":{"networks":{"shared":{"external":true}}}}`, "network shared: external is not allowed"},
		{"network driver", `{"top":{"networks":{"lan":{"driver":"macvlan"}}}}`, "network lan: driver is not allowed"},
		{"network name", `{"top":{"networks":{"n":{"name":"lf-edge-pe-other"}}}}`, "network n: name is not allowed"},
		{"undeclared network", `{"networks":{"lf-edge-pe-other":null}}`, "network lf-edge-pe-other is not declared"},
		{"network ipv4_address", `{"top":{"networks":{"n":{}}},"networks":{"n":{"ipv4_address":"172.30.0.5"}}}`, "networks.n: ipv4_address is not allowed"},
		{"external volume", `{"top":{"volumes":{"data":{"external":true}}}}`, "volume data: external is not allowed"},
		{"driver_opts volume", `{"top":{"volumes":{"data":{"driver_opts":{"type":"none","o":"bind","device":"/srv"}}}}}`, "volume data: driver_opts is not allowed"},
		{"named volume", `{"top":{"volumes":{"data":{"name":"victim_data"}}}}`, "volume data: name is not allowed"},
		// Escapes found in review (B1–B3, B4, S5).
		{"use_api_socket", `{"use_api_socket":true}`, "web: use_api_socket is not allowed"},
		{"provider", `{"provider":{"type":"/tmp/evil.sh"}}`, "web: provider is not allowed"},
		{"device_cgroup_rules", `{"device_cgroup_rules":["b *:* rmw"]}`, "web: device_cgroup_rules is not allowed"},
		{"sysctls", `{"sysctls":{"net.core.somaxconn":"1024"}}`, "web: sysctls is not allowed"},
		{"ulimits", `{"ulimits":{"memlock":-1}}`, "web: ulimits is not allowed"},
		{"runtime", `{"runtime":"runc"}`, "web: runtime is not allowed"},
		{"gpus", `{"gpus":"all"}`, "web: gpus is not allowed"},
		{"storage_opt", `{"storage_opt":{"size":"1T"}}`, "web: storage_opt is not allowed"},
		{"oom_score_adj", `{"oom_score_adj":-1000}`, "web: oom_score_adj is not allowed"},
		{"memswap_limit", `{"memswap_limit":-1}`, "web: memswap_limit is not allowed"},
		{"external_links", `{"external_links":["db"]}`, "web: external_links is not allowed"},
		{"extra_hosts", `{"extra_hosts":["x:1.2.3.4"]}`, "web: extra_hosts is not allowed"},
		{"security_opt", `{"security_opt":["seccomp=unconfined"]}`, "web: security_opt is not allowed"},
		{"credential_spec", `{"credential_spec":{"file":"x"}}`, "web: credential_spec is not allowed"},
		{"tmpfs", `{"tmpfs":["/run"]}`, "web: tmpfs is not allowed"},
		{"profiles", `{"profiles":["hidden"]}`, "web: profiles is not allowed"},
		{"volumes_from", `{"volumes_from":["container:abc"]}`, "web: volumes_from is not allowed"},
		{"deploy", `{"deploy":{"resources":{"limits":{"memory":"1g"}}}}`, "web: deploy is not allowed"},
		{"cache_to", `{"build":{"context":"/src","cache_to":["type=local,dest=/tmp/x"]}}`, "web: build: cache_to is not allowed"},
		{"cache_from", `{"build":{"context":"/src","cache_from":["type=local,src=/etc"]}}`, "web: build: cache_from is not allowed"},
		{"build ssh", `{"build":{"context":"/src","ssh":["default"]}}`, "web: build: ssh is not allowed"},
		{"build network", `{"build":{"context":"/src","network":"host"}}`, "web: build: network is not allowed"},
		{"build secrets", `{"build":{"context":"/src","secrets":["s"]}}`, "web: build: secrets is not allowed"},
		// Host paths compose reads.
		{"env_file outside", `{"env_file":[{"path":"/home/runner/.env","required":true}]}`, "env_file /home/runner/.env"},
		{"build context outside", `{"build":{"context":"/"}}`, "build context /"},
		{"build context url", `{"build":{"context":"https://example.com/repo.git"}}`, "build context https://example.com/repo.git"},
		{"dockerfile outside", `{"build":{"context":"/src","dockerfile":"../etc/Dockerfile"}}`, "Dockerfile /etc/Dockerfile"},
		{"additional context outside", `{"build":{"context":"/src","additional_contexts":{"x":"/etc"}}}`, "additional_contexts.x"},
		{"additional context url", `{"build":{"context":"/src","additional_contexts":{"x":"https://example.com/r.git"}}}`, "additional_contexts.x"},
		{"additional context image", `{"build":{"context":"/src","additional_contexts":{"x":"docker-image://alpine"}}}`, "additional_contexts.x"},
		// Top level.
		{"configs", `{"top":{"configs":{"c":{"file":"/etc/hosts"}}}}`, "top level: configs is not allowed"},
		{"secrets", `{"top":{"secrets":{"s":{"file":"/root/.ssh/id_rsa"}}}}`, "top level: secrets is not allowed"},
		{"models", `{"top":{"models":{"m":{"model":"x"}}}}`, "top level: models is not allowed"},
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
			web := cfg["services"].(map[string]any)["web"].(map[string]any)
			for k, v := range patch {
				if k != "top" {
					web[k] = v
				}
			}
			for k, v := range cfgMap(patch["top"]) {
				if v == nil {
					delete(cfg, k)
				} else {
					cfg[k] = v
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

func TestValidatePreviewComposeAllowsListedKeys(t *testing.T) {
	cfg := minimalPreviewConfig(t)
	web := cfg["services"].(map[string]any)["web"].(map[string]any)
	for k, v := range map[string]any{
		"env_file": []any{map[string]any{"path": "/src/.env.preview", "required": false}},
		"command":  nil, "entrypoint": nil, "user": "1000", "working_dir": "/app", "init": true, "read_only": false,
		"labels": map[string]any{"a": "b"}, "x-note": "ok",
		"networks": map[string]any{"default": nil, "backend": map[string]any{"aliases": []any{"api"}}},
		"build": map[string]any{"context": "/src", "dockerfile": "Dockerfile", "target": "prod", "args": map[string]any{"A": "1"},
			"additional_contexts": map[string]any{"assets": "/src/assets"}},
	} {
		web[k] = v
	}
	cfg["networks"] = map[string]any{"backend": map[string]any{"ipam": map[string]any{}, "internal": true}}
	cfg["volumes"] = map[string]any{"data": map[string]any{"labels": map[string]any{"k": "v"}}}
	cfg["name"] = "lf-pe-1"
	cfg["x-app"] = map[string]any{"anything": true}
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
	target := previewComposeTarget{Service: "web", Port: 8080, Services: []string{"seed", "web", "worker"}, Built: []string{"web"}, Networks: []string{"backend"}, Volumes: []string{"data"}}
	raw, err := previewComposeOverride("pe-0123456789", target)
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
	      "build": {"labels": {"lf.preview": "pe-0123456789", "com.docker.compose.project": "lf-pe-0123456789"}},
	      "networks": {"default": {}, "edge": {"aliases": ["lf-pe-0123456789"]}}},
	    "worker": {"mem_limit": "2g", "cpus": 2, "pids_limit": 512, "security_opt": ["no-new-privileges:true"],
	      "labels": {"lf.preview": "pe-0123456789"}, "networks": {"default": {}}}
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
