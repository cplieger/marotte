module github.com/cplieger/marotte

go 1.27.4

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/cplieger/go/httpcache v1.1.0 // indirect
	github.com/cplieger/go/httpclient v1.2.0 // indirect
	github.com/cplieger/go/jsonx v1.1.0 // indirect
	github.com/cplieger/go/scheduler v1.1.0 // indirect
	github.com/cplieger/go/urlform v1.2.0 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/expr-lang/expr v1.17.8 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)

require (
	github.com/cplieger/forgeapi/v2 v2.0.0
	github.com/cplieger/go/envx v1.1.0
	github.com/cplieger/go/filesafe v1.1.0
	github.com/cplieger/go/health/v2 v2.0.0
	github.com/cplieger/go/httpserver v1.5.0-dev.2
	github.com/cplieger/go/keyenc v1.3.0
	github.com/cplieger/go/keylock v1.1.0
	github.com/cplieger/go/measure v1.2.0
	github.com/cplieger/go/pathinside v1.1.0
	github.com/cplieger/go/slogx v1.1.0
	github.com/cplieger/go/sse/v2 v2.3.0-dev.4
	github.com/cplieger/go/ssrf v1.1.0
	github.com/cplieger/go/textsafe v1.1.0
	github.com/cplieger/go/wiregen v1.1.0-dev.2
	github.com/cplieger/go/yamlenv v1.1.0
	github.com/cplieger/toolbelt/v3 v3.16.0-dev.1
	github.com/cplieger/web-terminal-engine/v6 v6.5.0
	github.com/evanw/esbuild v0.28.2
	github.com/klauspost/compress v1.20.4
	github.com/tailscale/hujson v0.0.0-20260727124030-b80ff77dac4f
	go.yaml.in/yaml/v4 v4.0.0
	golang.org/x/image v0.48.0
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.25.0
	golang.org/x/sys v0.50.0
	golang.org/x/text v0.44.0
	pgregory.net/rapid v1.3.0
)

tool github.com/cplieger/toolbelt/v3/cmd/toolcatalog

ignore ./static-src/node_modules
