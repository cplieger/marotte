module github.com/cplieger/marotte

go 1.27.3

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/cplieger/httpx/v5 v5.2.0 // indirect
	github.com/cplieger/scheduler/v4 v4.3.0 // indirect
	github.com/cplieger/urlform v1.5.0 // indirect
	github.com/cplieger/webhttp/v3 v3.0.3 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/expr-lang/expr v1.17.8 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)

require (
	github.com/cplieger/atomicfile/v4 v4.2.0
	github.com/cplieger/envx/v2 v2.1.0
	github.com/cplieger/envx/yamlenv/v2 v2.3.0
	github.com/cplieger/forgeapi v1.4.0
	github.com/cplieger/jsoncap/v2 v2.1.0
	github.com/cplieger/keyenc v1.1.0
	github.com/cplieger/keylock v1.0.1
	github.com/cplieger/pathinside/v2 v2.1.0
	github.com/cplieger/pinstall/v3 v3.1.0
	github.com/cplieger/runesafe/v3 v3.2.0
	github.com/cplieger/slogx v1.8.0
	github.com/cplieger/sse v1.3.0
	github.com/cplieger/ssrf/v4 v4.5.0
	github.com/cplieger/toolbelt/v3 v3.11.0
	github.com/cplieger/web-terminal-engine/v6 v6.4.0
	github.com/cplieger/webhttp/v4 v4.0.0
	github.com/cplieger/wiregen/v3 v3.3.1
	github.com/evanw/esbuild v0.28.2
	github.com/klauspost/compress v1.20.3
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/image v0.47.0
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	pgregory.net/rapid v1.3.0
)

tool github.com/cplieger/toolbelt/v3/cmd/toolcatalog

ignore ./static-src/node_modules
