# check=error=true

FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f AS builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl && rm -rf /var/lib/apt/lists/*

# renovate: datasource=golang-version depName=golang
ARG GO_VERSION=1.27.1
RUN ARCH=$(dpkg --print-architecture) && \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" \
    | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# tsc is TypeScript 7's native compiler, shipped per platform as
# @typescript/typescript-linux-<arch>; only that tarball is fetched.
# renovate: datasource=npm depName=typescript
ARG TS_VERSION=7.0.2
# dpkg says amd64/arm64, the npm package says x64/arm64; a hardcoded x64
# fails the native arm64 build with "Exec format error".
RUN TS_ARCH=$([ "$(dpkg --print-architecture)" = "arm64" ] && echo "arm64" || echo "x64") && \
    curl -fsSL \
    "https://registry.npmjs.org/@typescript/typescript-linux-${TS_ARCH}/-/typescript-linux-${TS_ARCH}-${TS_VERSION}.tgz" \
    | tar -xz -C /tmp

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . ./

# The baked tool catalog is the first-boot/offline fallback; the engine
# refreshes it at boot and daily. The verify pass fails the build unless every
# required-tools.txt name resolves for linux amd64 and arm64.
ARG TOOL_CATALOG_URL=https://github.com/cplieger/tool-catalog/releases/latest/download/tool-catalog.json
# `go tool`, not `go run <pkg>@<version>`: the latter resolves outside the
# module and can fall through to GOPROXY=direct, which needs git this stage lacks.
# GOPROXY=off keeps the step offline.
RUN curl --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 20 --max-time 300 --retry 3 --retry-delay 5 -fsSL -o /tmp/tool-catalog.json "${TOOL_CATALOG_URL}" && \
    GOFLAGS=-mod=readonly GOPROXY=off go tool toolcatalog \
      verify -catalog /tmp/tool-catalog.json -require required-tools.txt \
      -overlay bundled-tools.json

# The @cplieger libraries publish TS source only; each is extracted into
# static-src/node_modules/@cplieger/<lib>/ for tsc and cmd/bundle to resolve.
# renovate: datasource=npm depName=@cplieger/actions
ARG CPLIEGER_ACTIONS_VERSION=3.1.7
RUN mkdir -p static-src/node_modules/@cplieger/actions && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/actions/-/actions-${CPLIEGER_ACTIONS_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/actions --strip-components=1

# renovate: datasource=npm depName=@cplieger/fetch
ARG CPLIEGER_FETCH_VERSION=2.2.2
RUN mkdir -p static-src/node_modules/@cplieger/fetch && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/fetch/-/fetch-${CPLIEGER_FETCH_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/fetch --strip-components=1

# renovate: datasource=npm depName=@cplieger/reactive
ARG CPLIEGER_REACTIVE_VERSION=2.1.2
RUN mkdir -p static-src/node_modules/@cplieger/reactive && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/reactive/-/reactive-${CPLIEGER_REACTIVE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/reactive --strip-components=1

# renovate: datasource=npm depName=@cplieger/web-terminal-engine
ARG CPLIEGER_WEB_TERMINAL_ENGINE_VERSION=6.1.0
RUN mkdir -p static-src/node_modules/@cplieger/web-terminal-engine && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/web-terminal-engine/-/web-terminal-engine-${CPLIEGER_WEB_TERMINAL_ENGINE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/web-terminal-engine --strip-components=1

# renovate: datasource=npm depName=@cplieger/web-terminal-ui
ARG CPLIEGER_WEB_TERMINAL_UI_VERSION=8.3.4
RUN mkdir -p static-src/node_modules/@cplieger/web-terminal-ui && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/web-terminal-ui/-/web-terminal-ui-${CPLIEGER_WEB_TERMINAL_UI_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/web-terminal-ui --strip-components=1

# css/ui-primitives.css is concatenated into static/style.css by cmd/bundle.
# This pin and static-src/package.json's track the same exact version.
# renovate: datasource=npm depName=@cplieger/ui-primitives
ARG CPLIEGER_UI_PRIMITIVES_VERSION=3.1.1
RUN mkdir -p static-src/node_modules/@cplieger/ui-primitives && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/ui-primitives/-/ui-primitives-${CPLIEGER_UI_PRIMITIVES_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/ui-primitives --strip-components=1

# This pin and static-src/package.json's track the same exact version.
# renovate: datasource=npm depName=@cplieger/keyenc
ARG CPLIEGER_KEYENC_VERSION=1.0.9
RUN mkdir -p static-src/node_modules/@cplieger/keyenc && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/keyenc/-/keyenc-${CPLIEGER_KEYENC_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/keyenc --strip-components=1

# This pin and static-src/package.json's track the same exact version.
# renovate: datasource=npm depName=@cplieger/sse
ARG CPLIEGER_SSE_VERSION=1.1.1
RUN mkdir -p static-src/node_modules/@cplieger/sse && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/sse/-/sse-${CPLIEGER_SSE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/sse --strip-components=1

# Every font URL is tag-pinned: releases/latest/download is mutable, so a sha
# gate over it would break on every upstream release.
#
# Monaspace Neon NF's SIL OFL 1.1 requires its licence to travel with every copy,
# and serving the woff2 files is a copy.
# renovate: datasource=github-tags depName=githubnext/monaspace
ARG MONASPACE_VERSION=v1.400
# repin: dep=githubnext/monaspace url=https://raw.githubusercontent.com/githubnext/monaspace/{version}/LICENSE dest=MonaspaceNeonNF-LICENSE
ARG MONASPACE_LICENSE_SHA256=0e84e5f7dd6f05e74a00f2fb828ca43e489d954f5509ff0fa439ea18c0d35fe9
# repin: dep=githubnext/monaspace url=https://raw.githubusercontent.com/githubnext/monaspace/{version}/fonts/Web%20Fonts/NerdFonts%20Web%20Fonts/Monaspace%20Neon/MonaspaceNeonNF-Regular.woff2
ARG MONASPACE_REGULAR_SHA256=8063ea45b6997c658035a4d876f996ecfa306c88fd0541d35d533fb1f9400c84
# repin: dep=githubnext/monaspace url=https://raw.githubusercontent.com/githubnext/monaspace/{version}/fonts/Web%20Fonts/NerdFonts%20Web%20Fonts/Monaspace%20Neon/MonaspaceNeonNF-Bold.woff2
ARG MONASPACE_BOLD_SHA256=45f56dceff8e569d61b6e3168fe208432e7bf0bc3e56e41b4d754cc575a063bd
# repin: dep=githubnext/monaspace url=https://raw.githubusercontent.com/githubnext/monaspace/{version}/fonts/Web%20Fonts/NerdFonts%20Web%20Fonts/Monaspace%20Neon/MonaspaceNeonNF-Italic.woff2
ARG MONASPACE_ITALIC_SHA256=3d77eb9a5ec9e32c5ac7ea49c4325e5d6c8e5fefda7317527de905130a88f3cf
# repin: dep=githubnext/monaspace url=https://raw.githubusercontent.com/githubnext/monaspace/{version}/fonts/Web%20Fonts/NerdFonts%20Web%20Fonts/Monaspace%20Neon/MonaspaceNeonNF-BoldItalic.woff2
ARG MONASPACE_BOLDITALIC_SHA256=5dffc9465be18eb63263671f1f3ba266ede49043cb6b3edcd65ea993c909b3aa

# web-terminal-glyphs carries no letters, digits or space, so 00-fonts.css lists
# it first and the text face keeps its metrics. Apache-2.0 section 4 requires its
# LICENSE and NOTICE to travel with it; each licence file is named for its family.
# renovate: datasource=github-releases depName=cplieger/web-terminal-glyphs
ARG WEB_TERMINAL_GLYPHS_VERSION=v1.1.7
# repin: dep=cplieger/web-terminal-glyphs url=https://github.com/cplieger/web-terminal-glyphs/releases/download/{version}/WebTerminalGlyphs.woff2
ARG WEB_TERMINAL_GLYPHS_SHA256=8f4720fa37eed4cdb3ca070d24fbbce85a5266b63c63e754d78a359509aeb94c
# repin: dep=cplieger/web-terminal-glyphs url=https://github.com/cplieger/web-terminal-glyphs/releases/download/{version}/LICENSE dest=WebTerminalGlyphs-LICENSE
ARG WEB_TERMINAL_GLYPHS_LICENSE_SHA256=c95bae1d1ce0235ecccd3560b772ec1efb97f348a79f0fbe0a634f0c2ccefe2c
# repin: dep=cplieger/web-terminal-glyphs url=https://github.com/cplieger/web-terminal-glyphs/releases/download/{version}/NOTICE dest=WebTerminalGlyphs-NOTICE
ARG WEB_TERMINAL_GLYPHS_NOTICE_SHA256=a5ac4badcc25b16fd3faed99e48caa48113a491cef3dbdd79d650243afefb4cb

# A for-loop's exit status is only its last iteration's, so each face is
# verified inside the loop. The `*)` arm fails the build for a face with no sha
# ARG.
RUN set -e; mkdir -p static/vendor/fonts; \
    for face in Regular Bold Italic BoldItalic; do \
      case "$face" in \
        Regular) face_sha="$MONASPACE_REGULAR_SHA256" ;; \
        Bold) face_sha="$MONASPACE_BOLD_SHA256" ;; \
        Italic) face_sha="$MONASPACE_ITALIC_SHA256" ;; \
        BoldItalic) face_sha="$MONASPACE_BOLDITALIC_SHA256" ;; \
        *) echo "ERROR font-sha-missing: no sha256 ARG for Monaspace face $face" >&2; exit 1 ;; \
      esac; \
      curl --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 20 --max-time 300 --retry 3 --retry-delay 5 -fsSL \
        -o "static/vendor/fonts/MonaspaceNeonNF-${face}.woff2" \
        "https://raw.githubusercontent.com/githubnext/monaspace/${MONASPACE_VERSION}/fonts/Web%20Fonts/NerdFonts%20Web%20Fonts/Monaspace%20Neon/MonaspaceNeonNF-${face}.woff2"; \
      printf '%s  static/vendor/fonts/MonaspaceNeonNF-%s.woff2\n' "$face_sha" "$face" | sha256sum -c -; \
    done; \
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 20 --max-time 300 --retry 3 --retry-delay 5 -fsSL \
      -o static/vendor/fonts/MonaspaceNeonNF-LICENSE \
      "https://raw.githubusercontent.com/githubnext/monaspace/${MONASPACE_VERSION}/LICENSE"; \
    printf '%s  static/vendor/fonts/MonaspaceNeonNF-LICENSE\n' "$MONASPACE_LICENSE_SHA256" | sha256sum -c -; \
    for asset in WebTerminalGlyphs.woff2 LICENSE NOTICE; do \
      case "$asset" in \
        WebTerminalGlyphs.woff2) asset_sha="$WEB_TERMINAL_GLYPHS_SHA256"; dest=WebTerminalGlyphs.woff2 ;; \
        LICENSE) asset_sha="$WEB_TERMINAL_GLYPHS_LICENSE_SHA256"; dest=WebTerminalGlyphs-LICENSE ;; \
        NOTICE) asset_sha="$WEB_TERMINAL_GLYPHS_NOTICE_SHA256"; dest=WebTerminalGlyphs-NOTICE ;; \
        *) echo "ERROR font-sha-missing: no sha256 ARG for glyph asset $asset" >&2; exit 1 ;; \
      esac; \
      curl --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 20 --max-time 300 --retry 3 --retry-delay 5 -fsSL \
        -o "static/vendor/fonts/${dest}" \
        "https://github.com/cplieger/web-terminal-glyphs/releases/download/${WEB_TERMINAL_GLYPHS_VERSION}/${asset}"; \
      printf '%s  static/vendor/fonts/%s\n' "$asset_sha" "$dest" | sha256sum -c -; \
    done

# BUILD_VERSION is stamped into internal/version.Build; the release workflow
# passes the tag. tsc --noEmit is the type gate (esbuild does not typecheck), then
# cmd/bundle bundles through esbuild's Go API.
ARG BUILD_VERSION=dev
# Wire-floor gate: go.mod's engine module and the npm client pin move
# independently, so assert both directional compatibility floors here. A mismatched
# pair closes every shell with code 4002 while /api/health stays green. The client
# floors come from the vendored wire-compatibility.json, the server's from
# scripts/wirecheck. Built, not `go run`: the exit code is the contract (0 ok,
# 1 floor violated, 2 gate broken), and `go run` collapses it to 1.
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=tmpfs,target=/tmp/wirecheck-bin \
    WIRE_MANIFEST=static-src/node_modules/@cplieger/web-terminal-engine/wire-compatibility.json && \
    test -f "$WIRE_MANIFEST" || { echo "wire-floor-gate: $WIRE_MANIFEST missing from the vendored engine artifact (fix the gate, do not bump a pin)" >&2; exit 2; } && \
    go build -o /tmp/wirecheck-bin/wirecheck ./scripts/wirecheck && \
    /tmp/wirecheck-bin/wirecheck -manifest "$WIRE_MANIFEST"

# hadolint ignore=DL3062
RUN /tmp/package/lib/tsc --project static-src/tsconfig.build.json --noEmit && \
    /tmp/package/lib/tsc --project static-src/tsconfig.sw.json --noEmit && \
    go run ./cmd/bundle

# The -X path must be go.mod's full module path: a wrong one is discarded
# silently and ships the "dev" fallback.
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X github.com/cplieger/marotte/internal/version.Build=${BUILD_VERSION}" \
    -o /app/marotte .

COPY scripts/collect-licenses.sh scripts/
RUN sh scripts/collect-licenses.sh --name marotte .

FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f

ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Only the minimal runtime surface is baked; everything else (Node, Python, Go,
# Java, Rust, LSPs) is installed on demand by the tools engine into /config/tools/.
# git serves gitexec, file history and forges; unzip the kiro-cli installer;
# xz-utils .tar.xz tool archives; jq entrypoint.sh. kiro-cli is downloaded at first
# boot because its licence forbids baking it. PKG_REFRESH busts this layer's cache, or `apt-get upgrade` never reruns and the
# image ships stale packages. The `echo` is required: BuildKit keys a RUN only on
# build args it consumes.
ARG PKG_REFRESH=static
# Re-declared because hadolint >= 2.15.0 drops the SHELL dialect at the next ARG.
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# libatomic1 is a runtime dependency of the Node.js the tools engine installs
# (official linux-x64 binaries link it from v25); without it every npm-sourced tool
# fails with exit status 127.
# hadolint ignore=DL3008
RUN echo "OS package refresh: ${PKG_REFRESH}" \
    && apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    jq \
    libatomic1 \
    openssh-client \
    tini \
    unzip \
    xz-utils \
    && rm -rf /var/lib/apt/lists/*

# KIRO_HOME must equal $HOME/.kiro: KAS ignores KIRO_HOME and uses os.homedir()/.kiro.
# GOROOT is unset: the toolchain derives it from its resolved location.
# tools/bin is the engine's only PATH dir; npm/ and python/ link their binaries into
# it, so their own bin dirs stay off PATH (they would sit ahead of /usr/bin as a
# plant target). tools/go/bin stays: it is GOPATH/bin for a hand-run `go install`.
ENV PATH="/config/tools/bin:/config/tools/go/bin:/config/home/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
ENV GOPATH="/config/tools/go"
ENV GOBIN="/config/tools/bin"
ENV HOME="/config/home"
ENV KIRO_HOME="/config/home/.kiro"
# Unset, glibc's C locale makes git octal-escape non-ASCII paths. C.UTF-8 is a
# glibc built-in, so no locales package is needed.
ENV LANG="C.UTF-8"
RUN mkdir -p /config/home/.kiro && chmod 777 /config/home /config/home/.kiro

# The composer upload directory (marotte.DefaultUploadDir), created at build
# time because a non-root runtime uid cannot create it at /. Sticky 1777 because
# the runtime uid is unknown. Not on a volume: mount one owned by the runtime uid to
# keep uploads across recreates.
RUN mkdir -p /uploads && chmod 1777 /uploads

# OpenSSH resolves "~" via getpwuid, not $HOME, so root's home must be the
# persisted /config/home or every recreate wipes known_hosts.
RUN sed -i 's|^root:x:0:0:root:/root:|root:x:0:0:root:/config/home:|' /etc/passwd

COPY --from=builder /app /app
COPY --from=builder /out/usr/share/licenses /usr/share/licenses

# /opt is hidden from the file browser (internal/filebrowse/paths.go).
COPY --chmod=755 entrypoint.sh /opt/marotte/entrypoint.sh
COPY --from=builder /tmp/tool-catalog.json /opt/marotte/tool-catalog.json
COPY bundled-tools.json /opt/marotte/bundled-tools.json

WORKDIR /workspace
EXPOSE 9847

# start-period=300s: a first boot downloads and verifies kiro-cli (~528 MB), and
# /api/health answers 503 until the pinned version is installed.
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=300s \
    CMD ["curl", "-sf", "http://127.0.0.1:9847/api/health"]
# tini is PID 1 so orphans are reaped: marotte waits only the children it
# started, and its git and kiro-cli grandchildren otherwise piled up as zombies
# (1,172 of 1,246 processes measured). entrypoint.sh keeps its `exec`, so the
# server stays tini's direct child and receives signals.
ENTRYPOINT ["/usr/bin/tini", "--", "/opt/marotte/entrypoint.sh"]
