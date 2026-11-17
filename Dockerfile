# syntax=docker/dockerfile:1
# The chatbot bundle and the Go binaries are architecture independent to build,
# so those stages run on the builder's own platform and Go cross-compiles for
# TARGETARCH. Only the final stage runs as the target (emulated for arm64 on an
# amd64 builder), which keeps multi-platform releases fast.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS chatbot
WORKDIR /src
COPY chatbot/package.json chatbot/package-lock.json chatbot/
RUN --mount=type=cache,target=/root/.npm npm --prefix chatbot ci --include=dev --no-audit --no-fund
COPY chatbot/ chatbot/
COPY web/app/ web/app/
RUN npm --prefix chatbot run build && test -f web/native-chat/index.html
FROM --platform=$BUILDPLATFORM golang:1.26.11-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
# Empty lets build-binaries.sh size parallelism from the builder's CPUs and memory.
ARG LOOPFORGE_BUILD_JOBS=
ENV LOOPFORGE_BUILD_JOBS=${LOOPFORGE_BUILD_JOBS}
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
# Ref updates must not invalidate the Go build cache.
COPY --exclude=.git . .
# Helpers do not embed the web UI, so they compile while the chatbot stage builds.
# The runner and gate binaries are built for every OS/arch regardless; GOOS and
# GOARCH pick the architecture of the binaries that run inside this image.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build GOOS=${TARGETOS} GOARCH=${TARGETARCH} bash scripts/build-binaries.sh --helpers /out
COPY --from=chatbot /src/web/native-chat/ web/native-chat/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build GOOS=${TARGETOS} GOARCH=${TARGETARCH} bash scripts/build-binaries.sh --platform /out
FROM node:22-bookworm
RUN --mount=type=cache,target=/root/.npm apt-get update && apt-get install -y --no-install-recommends ca-certificates git openssh-client bash curl python3 ripgrep \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @openai/codex@0.162.1 @tencent-ai/codebuddy-code@2.167.0 @deepseek-ai/dsh@0.2.2-rc.1
# A relocatable Python that sandboxes of images without python3 mount for the
# native CLI hooks (internal/runtime/toolchain.go). Its terminfo database has
# names differing only in case, which a case-insensitive data directory (macOS)
# cannot hold; the hooks never use curses.
ARG TARGETARCH
ARG PYTHON_STANDALONE=20261110/cpython-3.12.16+20261110
ARG PYTHON_STANDALONE_SHA256_AMD64=6a9e62dad033790aef722dbd370e0ce76ce17b73627ba3ae679f537049350b39
ARG PYTHON_STANDALONE_SHA256_ARM64=b6a41defec4975d94b4ff1950236e7383e5c02ba40ea48c9ec33c66087a35aab
RUN case "${TARGETARCH:-amd64}" in \
      amd64) arch=x86_64 sum="${PYTHON_STANDALONE_SHA256_AMD64}" ;; \
      arm64) arch=aarch64 sum="${PYTHON_STANDALONE_SHA256_ARM64}" ;; \
      *) echo "unsupported arch ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /tmp/python.tar.gz "https://github.com/astral-sh/python-build-standalone/releases/download/${PYTHON_STANDALONE}-${arch}-unknown-linux-gnu-install_only_stripped.tar.gz" \
 && echo "${sum}  /tmp/python.tar.gz" | sha256sum -c - \
 && mkdir -p /app \
 && tar -xzf /tmp/python.tar.gz -C /app \
 && rm -f /tmp/python.tar.gz \
 && rm -rf /app/python/share/terminfo \
 && /app/python/bin/python3 -c 'import json,urllib.request,ctypes'
# Kiro CLI, pinned and checked like the Python above (versions and sums match
# internal/runtime/kiro_login.go). Its 415 MB runtime unpacks into a shared
# read-only KIRO_RUNTIME_DIR here instead of each private HOME; an invalid key
# makes kiro-cli unpack it without a login and then exit.
ARG KIRO_CLI_VERSION=3.3.0
ARG KIRO_CLI_SHA256_AMD64=018e7d19124ebf5088bc33b12dcafa5c7e8de745ae491df024afebcd19f36b62
ARG KIRO_CLI_SHA256_ARM64=5e85a1fcc107f4cd72cbbf1f8e69463bf9673814014c0056636d8433150b18d4
ENV KIRO_RUNTIME_DIR=/opt/kiro-runtime
RUN case "${TARGETARCH:-amd64}" in \
      amd64) arch=x86_64 sum="${KIRO_CLI_SHA256_AMD64}" ;; \
      arm64) arch=aarch64 sum="${KIRO_CLI_SHA256_ARM64}" ;; \
      *) echo "unsupported arch ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /tmp/kirocli.zip "https://desktop-release.q.us-east-1.amazonaws.com/${KIRO_CLI_VERSION}/kirocli-${arch}-linux.zip" \
 && echo "${sum}  /tmp/kirocli.zip" | sha256sum -c - \
 && python3 -c 'import sys,zipfile; zipfile.ZipFile(sys.argv[1]).extract("kirocli/bin/kiro-cli", "/tmp/kirocli-x")' /tmp/kirocli.zip \
 && install -m 0755 /tmp/kirocli-x/kirocli/bin/kiro-cli /usr/local/bin/kiro-cli \
 && rm -rf /tmp/kirocli.zip /tmp/kirocli-x \
 && (HOME=/tmp/kiro-build KIRO_API_KEY=loopforge-build-unpack timeout 180 kiro-cli chat --v3 --no-interactive hi >/dev/null 2>&1 || true) \
 && ls -d "${KIRO_RUNTIME_DIR}/${KIRO_CLI_VERSION}"-* \
 && rm -rf /tmp/kiro-build "${KIRO_RUNTIME_DIR}/data.sqlite3" \
 && chmod -R a+rX "${KIRO_RUNTIME_DIR}"
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker:27-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY LICENSE THIRD_PARTY_NOTICES.md /app/
COPY third_party/go/ /app/third_party/go/
ENV PATH="/app/bin:${PATH}"
# An explicit LOOPFORGE_COMMIT wins. Otherwise read HEAD from the build context
# so `docker build` stamps the same corner label as the release workflow.
ARG LOOPFORGE_COMMIT=
RUN --mount=type=bind,source=.,target=/ctx,ro \
    sh /ctx/scripts/image-commit.sh /ctx /app/loopforge-commit
EXPOSE 8080
ENTRYPOINT ["/app/bin/agentflow"]
