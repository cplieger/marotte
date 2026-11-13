module github.com/cplieger/marotte

go 1.27.4

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/cplieger/go/httpcache v1.1.0-dev.5 // indirect
	github.com/cplieger/go/httpclient v1.2.0-dev.1 // indirect
	github.com/cplieger/go/jsonx v1.1.0-dev.1 // indirect
	github.com/cplieger/go/scheduler v1.1.0-dev.1 // indirect
	github.com/cplieger/go/urlform v1.2.0-dev.1 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/expr-lang/expr v1.17.8 // indirect
	github.com/fxamacker/cbor/v2 v2.10.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.9 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/webauthn v0.18.2 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/philhofer/fwd v1.2.1 // indirect
	github.com/tinylib/msgp v1.6.5 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/oauth2 v0.38.0 // indirect
)

require (
	github.com/cplieger/access v1.0.0-dev.8.0.20261112215528-356b9d582aaf
	github.com/cplieger/forgeapi/v2 v2.0.0-dev.18
	github.com/cplieger/go/envx v1.1.0-dev.2
	github.com/cplieger/go/filesafe v1.1.0-dev.1
	github.com/cplieger/go/health v1.1.0-dev.3
	github.com/cplieger/go/httpserver v1.2.0-dev.1
	github.com/cplieger/go/keyenc v1.3.0-dev.1
	github.com/cplieger/go/keylock v1.1.0-dev.1
	github.com/cplieger/go/measure v1.2.0-dev.1
	github.com/cplieger/go/pathinside v1.1.0-dev.1
	github.com/cplieger/go/slogx v1.1.0-dev.1
	github.com/cplieger/go/sse/v2 v2.1.0-dev.5
	github.com/cplieger/go/ssrf v1.1.0-dev.5
	github.com/cplieger/go/textsafe v1.1.0-dev.1
	github.com/cplieger/go/wiregen/v2 v2.0.0
	github.com/cplieger/go/yamlenv v1.1.0-dev.1
	github.com/cplieger/toolbelt/v3 v3.15.0-dev.32
	github.com/cplieger/web-terminal-engine/v6 v6.5.0-dev.12
	github.com/evanw/esbuild v0.28.2
	github.com/klauspost/compress v1.20.4
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/tailscale/hujson v0.0.0-20260727124030-b80ff77dac4f
	go.yaml.in/yaml/v4 v4.0.0
	golang.org/x/image v0.48.0
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.25.0
	golang.org/x/sys v0.50.0
	golang.org/x/text v0.44.0
	golang.org/x/tools v0.52.0
	pgregory.net/rapid v1.3.0
)

tool github.com/cplieger/toolbelt/v3/cmd/toolcatalog

ignore ./static-src/node_modules
