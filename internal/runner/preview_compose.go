package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Pure helpers for compose preview environments: validation of the repository
// compose file, the runner-owned override and the Caddy route.
// Design: docs/proposals/compose-preview-environments.md §4 and §6.

const (
	previewMetaKey   = "x-loopforge-preview"
	previewLabel     = "lf.preview"
	previewMemLimit  = "2g"
	previewCPUs      = 2.0
	previewPidsLimit = 512
)

var (
	previewEnvIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	previewHostPattern  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

func previewProject(envID string) string { return "lf-" + envID }
func previewEdge(envID string) string    { return "lf-edge-" + envID }

// previewComposeTarget is the entry service Caddy proxies to.
type previewComposeTarget struct {
	Service  string
	Port     int
	Services []string // every service, sorted
	Built    []string // services with a build section, sorted
	Networks []string // top-level networks declared by the repository, sorted
	Volumes  []string // top-level volumes declared by the repository, sorted
}

// Allowlists for the parsed output of `docker compose config --format json
// --no-normalize`. Anything not listed is rejected by name, so keys compose
// adds later (use_api_socket, provider services, device_cgroup_rules, …)
// fail closed. x-* extension keys are allowed wherever compose allows them.
var (
	previewTopLevelKeys = previewKeySet("name", "services", "networks", "volumes")
	previewServiceKeys  = previewKeySet("image", "build", "command", "entrypoint", "environment", "env_file", "expose",
		"depends_on", "volumes", "networks", "healthcheck", "restart", "working_dir", "user", "labels", "init",
		"stop_signal", "stop_grace_period", "tty", "stdin_open", "read_only")
	previewBuildKeys       = previewKeySet("context", "dockerfile", "args", "target", "additional_contexts", "labels")
	previewMountKeys       = previewKeySet("type", "source", "target", "read_only", "volume")
	previewMountVolumeKeys = previewKeySet("nocopy", "subpath")
	previewEnvFileKeys     = previewKeySet("path", "required", "format")
	previewServiceNetKeys  = previewKeySet("aliases")
	previewNetworkKeys     = previewKeySet("labels", "internal", "ipam")
	previewVolumeKeys      = previewKeySet("labels")
)

// validatePreviewCompose checks the parsed output of
// `docker compose config --format json --no-normalize` against §4 with
// allowlists. within reports whether a host path compose would read
// (env_file, build context, Dockerfile, additional contexts) stays inside the
// checkout. Every violation is returned so one deploy reports all of them.
func validatePreviewCompose(cfg map[string]any, within func(string) bool) (previewComposeTarget, []string) {
	var target previewComposeTarget
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	unknown := func(where string, obj map[string]any, allowed map[string]bool) {
		for _, key := range cfgKeys(obj) {
			if !allowed[key] && !strings.HasPrefix(key, "x-") && cfgPresent(obj[key]) {
				bad("%s: %s is not allowed", where, key)
			}
		}
	}

	unknown("top level", cfg, previewTopLevelKeys)
	services := cfgMap(cfg["services"])
	meta := cfgMap(cfg[previewMetaKey])
	if meta == nil {
		bad("%s is required (service and port of the entry service)", previewMetaKey)
	}
	target.Service, _ = meta["service"].(string)
	if _, ok := services[target.Service]; !ok {
		bad("%s.service %q is not a service", previewMetaKey, target.Service)
	}
	if port, ok := meta["port"].(float64); ok && port == float64(int(port)) && port >= 1 && port <= 65535 {
		target.Port = int(port)
	} else {
		bad("%s.port must be an integer between 1 and 65535", previewMetaKey)
	}

	networks := cfgMap(cfg["networks"])
	for _, name := range cfgKeys(networks) {
		network := cfgMap(networks[name])
		unknown("network "+name, network, previewNetworkKeys)
		if cfgSet(network["ipam"]) {
			bad("network %s: ipam is not allowed", name)
		}
		target.Networks = append(target.Networks, name)
	}
	volumes := cfgMap(cfg["volumes"])
	for _, name := range cfgKeys(volumes) {
		// name, external, driver and driver_opts could point at another
		// preview's or a host volume.
		unknown("volume "+name, cfgMap(volumes[name]), previewVolumeKeys)
		target.Volumes = append(target.Volumes, name)
	}

	for _, name := range cfgKeys(services) {
		target.Services = append(target.Services, name)
		svc := cfgMap(services[name])
		unknown(name, svc, previewServiceKeys)
		for i, raw := range cfgSlice(svc["volumes"]) {
			mount := cfgMap(raw)
			where := fmt.Sprintf("%s: volumes[%d]", name, i)
			unknown(where, mount, previewMountKeys)
			unknown(where+".volume", cfgMap(mount["volume"]), previewMountVolumeKeys)
			if kind := cfgString(mount["type"]); kind != "volume" {
				bad("%s: %s mount %s is not allowed (named volumes only)", name, kind, cfgString(mount["source"]))
			} else if source := cfgString(mount["source"]); source != "" && !previewHasKey(volumes, source) {
				bad("%s: volume %s is not declared in this file", name, source)
			}
		}
		netRefs := cfgMap(svc["networks"])
		for _, net := range cfgKeys(netRefs) {
			if net != "default" && !previewHasKey(networks, net) {
				bad("%s: network %s is not declared in this file", name, net)
			}
			unknown(name+": networks."+net, cfgMap(netRefs[net]), previewServiceNetKeys)
		}
		for _, raw := range cfgSlice(svc["env_file"]) {
			path := cfgString(raw)
			if entry := cfgMap(raw); entry != nil {
				unknown(name+": env_file", entry, previewEnvFileKeys)
				path = cfgString(entry["path"])
			}
			if path != "" && !(filepath.IsAbs(path) && within(path)) {
				bad("%s: env_file %s is outside the repository", name, path)
			}
		}
		if build := cfgMap(svc["build"]); build != nil {
			target.Built = append(target.Built, name)
			unknown(name+": build", build, previewBuildKeys)
			context := cfgString(build["context"])
			if !(filepath.IsAbs(context) && within(context)) {
				bad("%s: build context %s is outside the repository", name, context)
			}
			if dockerfile := cfgString(build["dockerfile"]); dockerfile != "" {
				if !filepath.IsAbs(dockerfile) {
					dockerfile = filepath.Join(context, dockerfile)
				}
				if !within(dockerfile) {
					bad("%s: Dockerfile %s is outside the repository", name, dockerfile)
				}
			}
			contexts := cfgMap(build["additional_contexts"])
			for _, key := range cfgKeys(contexts) {
				// compose resolves relative paths; URLs and image refs are not paths.
				if value := cfgString(contexts[key]); !(filepath.IsAbs(value) && within(value)) {
					bad("%s: build additional_contexts.%s %s must be a path inside the repository", name, key, value)
				}
			}
		}
	}
	return target, errs
}

// previewComposeOverride is the runner-owned compose file applied after the
// repository's: resource limits on every service, every repository network
// internal, and only the entry service on the preview's edge network under
// the alias lf-<envId>. Everything is labelled lf.preview=<envId> for the
// teardown leftover count. JSON is valid YAML.
func previewComposeOverride(envID string, target previewComposeTarget) ([]byte, error) {
	labels := map[string]string{previewLabel: envID}
	services := map[string]any{}
	for _, name := range target.Services {
		svc := map[string]any{
			"mem_limit":    previewMemLimit,
			"cpus":         previewCPUs,
			"pids_limit":   previewPidsLimit,
			"security_opt": []string{"no-new-privileges:true"},
			"labels":       labels,
		}
		if slices.Contains(target.Built, name) {
			// Built images carry no compose labels; these let teardown find them.
			svc["build"] = map[string]any{"labels": map[string]string{previewLabel: envID, "com.docker.compose.project": previewProject(envID)}}
		}
		nets := map[string]any{"default": map[string]any{}}
		if name == target.Service {
			nets["edge"] = map[string]any{"aliases": []string{previewProject(envID)}}
		}
		svc["networks"] = nets
		services[name] = svc
	}
	// internal: no route out of the preview and no path to host services.
	networks := map[string]any{
		"default": map[string]any{"internal": true, "labels": labels},
		"edge":    map[string]any{"name": previewEdge(envID), "external": true},
	}
	for _, name := range target.Networks {
		networks[name] = map[string]any{"internal": true, "labels": labels}
	}
	doc := map[string]any{"services": services, "networks": networks}
	if len(target.Volumes) > 0 {
		volumes := map[string]any{}
		for _, name := range target.Volumes {
			volumes[name] = map[string]any{"labels": labels}
		}
		doc["volumes"] = volumes
	}
	return json.MarshalIndent(doc, "", "  ")
}

// previewCaddyRoute is the per-preview Caddyfile site imported by the edge
// Caddy. host and envID are validated by the caller; they end up in Caddy syntax.
func previewCaddyRoute(host, authUpstream, envID string, port int) string {
	return fmt.Sprintf("http://%s {\n\tforward_auth %s {\n\t\turi /verify?env=%s\n\t}\n\treverse_proxy %s:%d\n}\n",
		host, authUpstream, envID, previewProject(envID), port)
}

// previewPathWithin reports whether path is root or below it, lexically.
func previewPathWithin(root string) func(string) bool {
	return func(path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
}

func previewKeySet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func previewHasKey(m map[string]any, key string) bool { _, ok := m[key]; return ok }

// cfgPresent treats only JSON null as absent: compose prints command and
// entrypoint as null, while an explicit false or empty value is still a key
// the repository chose to set.
func cfgPresent(v any) bool { return v != nil }

func cfgMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func cfgSlice(v any) []any        { s, _ := v.([]any); return s }
func cfgString(v any) string      { s, _ := v.(string); return strings.TrimSpace(s) }

// cfgSet treats false, zero, empty strings and empty collections as unset,
// matching how compose omits defaults.
func cfgSet(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return strings.TrimSpace(x) != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func cfgKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
