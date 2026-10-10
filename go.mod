module github.com/cplieger/marotte

go 1.27.2

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/cplieger/scheduler/v4 v4.2.3 // indirect
	github.com/cplieger/urlform v1.4.0 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/expr-lang/expr v1.17.8 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)

require (
	github.com/cplieger/atomicfile/v4 v4.1.0
	github.com/cplieger/envx/v2 v2.0.7
	github.com/cplieger/envx/yamlenv/v2 v2.0.3
	github.com/cplieger/forgeapi v1.1.0-dev.6
	github.com/cplieger/httpx/v5 v5.0.5
	github.com/cplieger/jsoncap/v2 v2.0.3
	github.com/cplieger/keyenc v1.1.0-dev.1
	github.com/cplieger/pathinside/v2 v2.0.3
	github.com/cplieger/pinstall/v3 v3.0.8
	github.com/cplieger/runesafe/v2 v2.1.1
	github.com/cplieger/slogx v1.6.7
	github.com/cplieger/sse v1.2.0
	github.com/cplieger/ssrf/v4 v4.3.0-dev.1
	github.com/cplieger/toolbelt/v3 v3.8.1
	github.com/cplieger/web-terminal-engine/v6 v6.2.0-dev.7
	github.com/cplieger/webhttp/v3 v3.0.2
	github.com/cplieger/wiregen/v3 v3.3.0-dev.2
	github.com/evanw/esbuild v0.28.2
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/image v0.47.0
	golang.org/x/net v0.61.0
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	pgregory.net/rapid v1.3.0
)

tool github.com/cplieger/toolbelt/v3/cmd/toolcatalog

ignore ./static-src/node_modules
