-- Idempotent MySQL migration: add python3-pip and python3-venv to the
-- built-in generic-backend Dockerfile.
--
-- A test role could not install a Python project's test tools (pytest and so
-- on) in the shared sandbox. The platform image (Dockerfile) and
-- runtime.IsolatedDockerfile gained both packages; the seeded row has to match
-- IsolatedDockerfile byte for byte, otherwise a generic-docker sandbox builds
-- a separate agentflow-img-* image on first use. Only the unmodified previous
-- default (the text 031 writes) is rewritten, so an edited Dockerfile is left
-- alone. scripts/install-schema-extensions.sh runs this after 031 on install
-- and on ./start.sh pull.

UPDATE dockerfiles
SET payload = JSON_SET(payload, '$.content', 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git openssh-client python3 python3-pip python3-venv ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n')
WHERE id = 'generic-backend'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) = 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git openssh-client python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n';
