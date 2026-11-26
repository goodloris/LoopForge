package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
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
	Networks []string // top-level networks declared by the repository, sorted
	Volumes  []string // top-level volumes declared by the repository, sorted
}

// validatePreviewCompose checks the parsed output of
// `docker compose config --format json --no-normalize` against §4. within
// reports whether a host path the runner would read (env_file, configs,
// secrets, build context, Dockerfile) stays inside the checkout. Every
// violation is returned so one deploy reports all of them.
func validatePreviewCompose(cfg map[string]any, within func(string) bool) (previewComposeTarget, []string) {
	var target previewComposeTarget
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

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
		if cfgSet(cfgMap(networks[name])["external"]) {
			bad("network %s: external networks are not allowed", name)
			continue
		}
		target.Networks = append(target.Networks, name)
	}
	volumes := cfgMap(cfg["volumes"])
	for _, name := range cfgKeys(volumes) {
		vol := cfgMap(volumes[name])
		if cfgSet(vol["external"]) {
			bad("volume %s: external volumes are not allowed", name)
		}
		if cfgSet(vol["driver_opts"]) {
			bad("volume %s: driver_opts are not allowed", name)
		}
		target.Volumes = append(target.Volumes, name)
	}
	// configs and secrets with a file source are host bind mounts.
	for _, kind := range []string{"configs", "secrets"} {
		defs := cfgMap(cfg[kind])
		for _, name := range cfgKeys(defs) {
			def := cfgMap(defs[name])
			if file, _ := def["file"].(string); file != "" && !within(file) {
				bad("%s %s: file %s is outside the repository", kind, name, file)
			}
			if cfgSet(def["external"]) {
				bad("%s %s: external %s are not allowed", kind, name, kind)
			}
		}
	}

	for _, name := range cfgKeys(services) {
		target.Services = append(target.Services, name)
		svc := cfgMap(services[name])
		if cfgSet(svc["ports"]) {
			bad("%s: ports are not allowed (use expose)", name)
		}
		for _, key := range []string{"privileged", "cap_add", "devices", "container_name", "userns_mode", "cgroup_parent", "security_opt"} {
			if cfgSet(svc[key]) {
				bad("%s: %s is not allowed", name, key)
			}
		}
		for _, key := range []string{"pid", "ipc", "uts", "cgroup"} {
			if value := cfgString(svc[key]); value == "host" || strings.HasPrefix(value, "container:") {
				bad("%s: %s=%s is not allowed", name, key, value)
			}
		}
		// Only "none" and "service:<x>" keep the service off host and default
		// bridge networking, where the internal network would not apply.
		if mode := cfgString(svc["network_mode"]); mode != "" {
			if mode != "none" && !strings.HasPrefix(mode, "service:") {
				bad("%s: network_mode=%s is not allowed", name, mode)
			} else if name == target.Service {
				bad("%s: the entry service cannot set network_mode", name)
			}
		}
		for _, raw := range cfgSlice(svc["volumes"]) {
			vol := cfgMap(raw)
			if kind := cfgString(vol["type"]); kind != "volume" {
				bad("%s: %s mount %s is not allowed (named volumes only)", name, kind, cfgString(vol["source"]))
			}
		}
		for _, raw := range cfgSlice(svc["volumes_from"]) {
			if from := cfgString(raw); strings.HasPrefix(from, "container:") {
				bad("%s: volumes_from %s is not allowed", name, from)
			}
		}
		for _, raw := range cfgSlice(svc["env_file"]) {
			path := cfgString(raw)
			if entry := cfgMap(raw); entry != nil {
				path = cfgString(entry["path"])
			}
			if path != "" && !within(path) {
				bad("%s: env_file %s is outside the repository", name, path)
			}
		}
		if build := cfgMap(svc["build"]); build != nil {
			context := cfgString(build["context"])
			if context != "" && !within(context) {
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
			for _, key := range cfgKeys(cfgMap(build["additional_contexts"])) {
				if value := cfgString(cfgMap(build["additional_contexts"])[key]); filepath.IsAbs(value) && !within(value) {
					bad("%s: build additional_contexts.%s %s is outside the repository", name, key, value)
				}
			}
			if cfgSet(build["ssh"]) {
				bad("%s: build ssh is not allowed", name)
			}
			if network := cfgString(build["network"]); network == "host" {
				bad("%s: build network=host is not allowed", name)
			}
			if cfgSet(build["privileged"]) || cfgSet(build["entitlements"]) {
				bad("%s: privileged builds are not allowed", name)
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
func previewComposeOverride(envID string, target previewComposeTarget, networkModes map[string]bool) ([]byte, error) {
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
		if !networkModes[name] {
			nets := map[string]any{"default": map[string]any{}}
			if name == target.Service {
				nets["edge"] = map[string]any{"aliases": []string{previewProject(envID)}}
			}
			svc["networks"] = nets
		}
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

// previewNetworkModes lists services that set network_mode; compose rejects
// networks on them, so the override leaves their networking alone.
func previewNetworkModes(cfg map[string]any) map[string]bool {
	modes := map[string]bool{}
	for name, raw := range cfgMap(cfg["services"]) {
		if cfgString(cfgMap(raw)["network_mode"]) != "" {
			modes[name] = true
		}
	}
	return modes
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
