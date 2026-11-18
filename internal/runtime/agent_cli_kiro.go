package runtime

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
)

var kiroServerIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// kiroStream: `kiro-cli chat --v3 --output-format stream-json` wraps ACP
// session/update notifications ({"type":"sessionUpdate","data":{"sessionId",
// "update"}}) between runStarted and runFinished / runError.
// Kiro reports subscription credits, not tokens (TokenUsage.Credits).
type kiroStream struct {
	started  bool
	segment  int
	text     strings.Builder
	thinking strings.Builder
	failure  string
	// credits is this invocation's, sessionCredits the session total.
	credits, sessionCredits float64
}

func (k *kiroStream) flush(s *ClaudeStream) []string {
	var out []string
	if text := k.thinking.String(); text != "" {
		k.thinking.Reset()
		out = append(out, s.Thinking(text)...)
	}
	if text := k.text.String(); text != "" {
		k.text.Reset()
		out = append(out, s.Text(text)...)
	}
	k.segment++
	return out
}

// kiroToolName names an ACP tool call: MCP tools as mcp__<server>__<tool>
// from their "@server/tool" title, built-in tools by the tool ID Kiro puts
// ahead of the model's call ID (run_command_toolu_…).
func kiroToolName(update map[string]any) string {
	meta, _ := update["_meta"].(map[string]any)
	kiro, _ := meta["kiro"].(map[string]any)
	title := stringValue(update["title"])
	if server := stringValue(kiro["serverName"]); server != "" {
		tool := title
		if _, rest, ok := strings.Cut(title, "/"); ok {
			tool = rest
		}
		return "mcp__" + server + "__" + tool
	}
	if prefix, _, ok := strings.Cut(stringValue(update["toolCallId"]), "_toolu_"); ok && prefix != "" {
		return prefix
	}
	return title
}

// kiroToolCall names a call the way the Claude-style pipeline reads it: a
// sub-agent dispatch becomes Task (subagent_type, prompt), a skill load
// becomes Skill, and Kiro's schema-loading meta tools are dropped ("").
func kiroToolCall(update map[string]any) (string, any) {
	input := update["rawInput"]
	raw, _ := input.(map[string]any)
	resource := stringValue(raw["id"])
	switch name := kiroToolName(update); {
	case name == "invoke_subagent":
		return "Task", map[string]any{"subagent_type": stringValue(raw["name"]), "prompt": stringValue(raw["prompt"])}
	case strings.HasPrefix(resource, "skill::"):
		return "Skill", map[string]any{"skill": strings.TrimPrefix(resource, "skill::")}
	case strings.HasPrefix(resource, "tool::") || name == "Tool Load" || name == "Load Resource":
		return "", nil
	default:
		return name, input
	}
}

// kiroToolOutput joins the text of ACP tool call content
// ([{"type":"content","content":{"type":"text","text"}}]).
func kiroToolOutput(update map[string]any) string {
	var parts []string
	for _, item := range sliceValue(update["content"]) {
		entry, _ := item.(map[string]any)
		if inner, ok := entry["content"].(map[string]any); ok {
			parts = append(parts, contentText([]any{inner}))
		}
	}
	if len(parts) == 0 && update["rawOutput"] != nil {
		return jsonText(update["rawOutput"])
	}
	return strings.Join(parts, "\n")
}

func (k *kiroStream) translate(s *ClaudeStream, raw map[string]any) []string {
	data, _ := raw["data"].(map[string]any)
	var out []string
	if session := stringValue(data["sessionId"]); session != "" && !k.started {
		k.started = true
		out = s.Init(session, "")
	}
	switch stringValue(raw["type"]) {
	case "sessionUpdate":
		update, _ := data["update"].(map[string]any)
		switch stringValue(update["sessionUpdate"]) {
		case "agent_thought_chunk":
			content, _ := update["content"].(map[string]any)
			text := stringValue(content["text"])
			k.thinking.WriteString(text)
			out = append(out, s.Delta(fmt.Sprintf("thinking#%d", k.segment), "thinking", text)...)
		case "agent_message_chunk":
			content, _ := update["content"].(map[string]any)
			text := stringValue(content["text"])
			if k.thinking.Len() > 0 {
				// Reasoning ends where the answer starts.
				thought := k.thinking.String()
				k.thinking.Reset()
				out = append(out, s.Thinking(thought)...)
				k.segment++
			}
			k.text.WriteString(text)
			out = append(out, s.Delta(fmt.Sprintf("text#%d", k.segment), "text", text)...)
		case "tool_call", "tool_call_update":
			out = append(out, k.flush(s)...)
			id := stringValue(update["toolCallId"])
			name, input := kiroToolCall(update)
			if name == "" {
				// Tool and resource loading only discloses schemas.
				break
			}
			out = append(out, s.ToolUse(id, name, input)...)
			switch status := stringValue(update["status"]); status {
			case "completed", "failed":
				out = append(out, s.ToolResult(id, status == "failed", kiroToolOutput(update))...)
			}
		case "session_info_update":
			meta, _ := update["_meta"].(map[string]any)
			kiro, _ := meta["kiro"].(map[string]any)
			if display, ok := kiro["displayError"].(map[string]any); ok {
				k.failure = stringValue(display["message"])
			}
			// usage.credits is the session total (session_usage), which the
			// engine's per-session cursor turns into each SOP invocation's
			// share; usage.turn_credits is this invocation's, which chat
			// turns add up (promptTurnSummaries).
			switch stringValue(kiro["kind"]) {
			case "turn_completion":
				for _, item := range sliceValue(kiro["promptTurnSummaries"]) {
					entry, _ := item.(map[string]any)
					if stringValue(entry["unit"]) == "credit" {
						k.credits += float64Value(entry["usage"])
					}
				}
			case "session_usage":
				k.sessionCredits = 0
				for _, item := range sliceValue(kiro["credits"]) {
					entry, _ := item.(map[string]any)
					if stringValue(entry["unit"]) == "credit" {
						k.sessionCredits += float64Value(entry["usage"])
					}
				}
			}
			session := k.sessionCredits
			if session < k.credits {
				session = k.credits
			}
			s.SetCredits(session)
			s.SetTurnCredits(k.credits)
		}
	case "runFinished":
		out = append(out, k.flush(s)...)
		failed := stringValue(data["status"]) != "success"
		text := stringValue(data["finalText"])
		if failed && text == "" {
			text = k.failure
			if text == "" {
				text = "Kiro CLI 运行未成功：" + stringValue(data["stopReason"])
			}
		}
		out = append(out, s.Result(text, failed)...)
	case "runError":
		out = append(out, k.flush(s)...)
		text := stringValue(data["message"])
		if text == "" {
			text = k.failure
		}
		out = append(out, s.Result(text, true)...)
	}
	return out
}

// applyKiroLogin runs Kiro CLI on a subscription login (`kiro-cli login`).
// KIRO_HOME moves ~/.kiro (MCP, steering, agents, hooks, sessions) into the
// private HOME. Without a sandbox HOME stays the machine's own, so Kiro uses
// and refreshes the login in its data directory. In any sandbox, and on a
// platform that only mounts the login (AGENTFLOW_KIRO_DATA_DIR), Kiro runs on
// a copy of just the login (copyKiroLogin) with HOME the private HOME, so no
// sandboxed agent can read the machine's conversations or write its login.
// KIRO_HOME is undocumented; it was verified against kiro-cli 3.3.0 and 3.4.0.
func applyKiroLogin(options AgentCLIOptions, home AgentCLIHome, homeEnv string, argv []string) ([]string, map[string]string, error) {
	env := map[string]string{"KIRO_HOME": homeEnv + "/.kiro", "KIRO_NO_AUTO_UPDATE": "1"}
	// Tool search hides MCP tool schemas behind tool_load once they pass a
	// size threshold, and a model then sometimes reports a platform tool
	// such as submit_assistant_reply as unavailable. Rewritten every run,
	// so a workspace turn cannot change it.
	if err := writeAgentCLIFile(home, ".kiro/settings/cli.json", []byte(`{"toolSearch.enabled":false}`)); err != nil {
		return nil, nil, err
	}
	if !options.Sandboxed && !options.Docker && strings.TrimSpace(os.Getenv(kiroDataDirEnv)) == "" {
		if machine, err := os.UserHomeDir(); err == nil && machine != "" {
			env["HOME"] = machine
		}
		return argv, env, nil
	}
	rel := kiroCopyDataRel
	if !options.Docker {
		// A process sandbox runs this machine's kiro-cli, which looks for
		// its database where this OS keeps it.
		rel = kiroHostDataRel()
		env["XDG_DATA_HOME"] = homeEnv + "/.local/share"
	}
	if err := copyKiroLogin(home, rel); err != nil {
		return nil, nil, err
	}
	env["HOME"] = homeEnv
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if options.Docker {
		// The runtime unpacks once per run into the workspace, unless the
		// image ships one in KIRO_RUNTIME_DIR.
		// ponytail: about 20 s per run, because a shared writable cache
		// would let one run's agent alter another's runtime.
		env["AGENTFLOW_KIRO_RUNTIME"] = "/workspace/.agentflow/kiro-runtime"
		// An image without kiro-cli (a custom Dockerfile, or a sandbox an
		// execution machine builds) runs the machine's cached copy from the
		// read-only RuntimeCLIDir mount; without one it installs the pinned
		// release into the private HOME.
		if strings.TrimSpace(os.Getenv("AGENTFLOW_ISOLATED_IMAGE")) == "" && RuntimeCLIDir() != "" {
			if cached, err := EnsureKiroCLICache(ctx); err == nil {
				env["AGENTFLOW_KIRO_CLI"] = cached
			} else {
				log.Printf("kiro-cli cache unavailable, sandbox installs its own: %v", err)
			}
		}
	} else if runtimeDir, err := ensureKiroHostRuntime(ctx); err == nil {
		// Read-only use of a runtime this machine unpacked once.
		env["AGENTFLOW_KIRO_RUNTIME"] = runtimeDir
	} else {
		log.Printf("kiro-cli runtime cache unavailable, the private HOME unpacks its own: %v", err)
	}
	// A copy refreshes its own token; an expired one can fail the first
	// prompt of a fresh database, so a model listing refreshes it first.
	// Session leases name the PID and host of the container that wrote them,
	// which a later container cannot check, so they would refuse --resume-id.
	// ponytail: removing them assumes the platform never runs two turns of
	// one Kiro session at once, which its per-session queue guarantees.
	// Nothing under HOME goes before the trusted PATH and system tools run
	// by absolute path: a conversation keeps its HOME across turns, and an
	// executable a workspace turn planted there must not run in a later
	// platform-mode turn. Only a Docker sandbox, which has no trusted copy
	// to shadow, falls back to installing kiro-cli into HOME.
	install := `command -v "$1" >/dev/null 2>&1 || { echo "kiro-cli 未安装" >&2; exit 127; }`
	if options.Docker {
		install = `command -v "$1" >/dev/null 2>&1 || PATH="$PATH:$HOME/.local/bin"
command -v "$1" >/dev/null 2>&1 || {
` + kiroInstallScript(`"$HOME/.local/bin"`) + `
} >&2 || exit 1`
	}
	script := `if [ -z "${KIRO_RUNTIME_DIR:-}" ] && [ -n "${AGENTFLOW_KIRO_RUNTIME:-}" ]; then KIRO_RUNTIME_DIR=$AGENTFLOW_KIRO_RUNTIME; export KIRO_RUNTIME_DIR; fi
if ! command -v "$1" >/dev/null 2>&1 && [ -x "${AGENTFLOW_KIRO_CLI:-}" ]; then PATH="${AGENTFLOW_KIRO_CLI%/*}:$PATH"; fi
` + install + `
export PATH
/bin/rm -f "$KIRO_HOME"/session-leases/*.lease 2>/dev/null
"$1" chat --list-models --format json >/dev/null 2>&1
exec "$@"`
	return append([]string{"sh", "-c", script, "kiro"}, argv...), env, nil
}

// kiroConversationKeep is what a conversation's KIRO_HOME keeps between
// turns: native sessions and login state, plus what the platform rewrites
// before every turn (MCP settings, role agents, skills).
var kiroConversationKeep = map[string]bool{
	"sessions": true, "session-index": true, "session-leases": true, "logs": true,
	"identities": true, "cloud-cache": true, "enterprise-management": true,
	"agents": true, "skills": true, "settings": true,
}

// resetKiroConversationHome removes everything else from KIRO_HOME, the
// MCP settings file aside: a workspace turn can write the home, and hooks,
// permission rules, steering or Powers it planted there would otherwise load
// in a later platform-mode turn. Kiro reads hooks from every file in
// KIRO_HOME/hooks, so guarding one file would not be enough.
func resetKiroConversationHome(home AgentCLIHome) error {
	root, err := openAgentCLIHome(home)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(".kiro", 0o700); err != nil {
		return err
	}
	clear := func(dir string, keep func(string) bool) error {
		f, err := root.Open(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		names, err := f.Readdirnames(-1)
		f.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			if !keep(name) {
				if err := root.RemoveAll(dir + "/" + name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := clear(".kiro", func(name string) bool { return kiroConversationKeep[name] }); err != nil {
		return err
	}
	return clear(".kiro/settings", func(name string) bool { return name == "mcp.json" || name == "cli.json" })
}
