package httpapi

import (
	"log"
	"os"
	"slices"
	"strings"
	"sync"
)

// assistantDefaultExecutorEnv names the deployment's default CLI.
const assistantDefaultExecutorEnv = "AGENTFLOW_DEFAULT_EXECUTOR"

// assistantDefaultExecutorWarned remembers the invalid values already logged,
// so a bad setting warns once per distinct value instead of on every request.
var assistantDefaultExecutorWarned sync.Map

// assistantDefaultExecutor is the CLI a conversation or request runs on when
// nothing names one. AGENTFLOW_DEFAULT_EXECUTOR overrides it; an unknown value
// falls back to CodeBuddy and is logged once per distinct value. The variable
// is read on every call so tests and operators never see a stale value.
func assistantDefaultExecutor() string {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(assistantDefaultExecutorEnv)))
	if raw == "" {
		return assistantEngineCodeBuddy
	}
	if raw == "claude" {
		raw = assistantEngineClaude
	}
	valid := append([]string{assistantEngineCodeBuddy}, assistantCLIEngines()...)
	if slices.Contains(valid, raw) {
		return raw
	}
	if _, seen := assistantDefaultExecutorWarned.LoadOrStore(raw, true); !seen {
		log.Printf("%s=%q is not a supported executor (%s); using %s", assistantDefaultExecutorEnv, raw, strings.Join(valid, ", "), assistantEngineCodeBuddy)
	}
	return assistantEngineCodeBuddy
}

// assistantDefaultRequestModel fills a request that names neither a CLI nor
// a model with the deployment's default CLI. Any other input is the caller's
// choice and is kept; CodeBuddy as default keeps the old empty model.
func assistantDefaultRequestModel(executor, model string) string {
	if strings.TrimSpace(executor) != "" || strings.TrimSpace(model) != "" {
		return model
	}
	if engine := assistantDefaultExecutor(); engine != assistantEngineCodeBuddy {
		return engine + ":default"
	}
	return model
}
