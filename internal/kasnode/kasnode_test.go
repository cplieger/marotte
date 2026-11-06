package kasnode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLaunch_PutsTheFlagsAheadOfKiroCLIsArguments(t *testing.T) {
	environ := []string{"PATH=/bin", LaunchVar + "=/data/kiro-cli/node", NodePathVar + "=/app/marotte", "NODE_OPTIONS=--max-old-space-size=4096"}
	node, argv, env, err := launch(environ, []string{"/app/marotte", "--experimental-wasm-modules", "acp-server.js", "--transport=stdio"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if node != "/data/kiro-cli/node" {
		t.Errorf("node = %q, want the path %s names", node, LaunchVar)
	}
	want := []string{
		"/data/kiro-cli/node", "--experimental-async-context-frame", "--max-semi-space-size=64",
		"--experimental-wasm-modules", "acp-server.js", "--transport=stdio",
	}
	if !slices.Equal(argv, want) {
		t.Errorf("argv = %q, want %q", argv, want)
	}
	// Neither variable may reach what the agent server spawns: a nested kiro-cli would run marotte as its node.
	if wantEnv := []string{"PATH=/bin", "NODE_OPTIONS=--max-old-space-size=4096"}; !slices.Equal(env, wantEnv) {
		t.Errorf("env = %q, want %q", env, wantEnv)
	}
	for _, kv := range env {
		if strings.Contains(kv, "--experimental-async-context-frame") {
			t.Errorf("env %q carries the async-context flag; Node 24 and later refuse it in NODE_OPTIONS", kv)
		}
	}
}

// The heap limit rides node's argv after Flags, and its variable leaves the environment with the other two.
func TestLaunch_PutsTheHeapLimitOnArgv(t *testing.T) {
	environ := []string{"PATH=/bin", LaunchVar + "=/data/kiro-cli/node", HeapLimitVar + "=2048", "NODE_OPTIONS=--trace-warnings"}
	_, argv, env, err := launch(environ, []string{"/app/marotte", "acp-server.js"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	want := []string{
		"/data/kiro-cli/node", "--experimental-async-context-frame", "--max-semi-space-size=64",
		"--max-old-space-size=2048", "acp-server.js",
	}
	if !slices.Equal(argv, want) {
		t.Errorf("argv = %q, want %q", argv, want)
	}
	if wantEnv := []string{"PATH=/bin", "NODE_OPTIONS=--trace-warnings"}; !slices.Equal(env, wantEnv) {
		t.Errorf("env = %q, want %q: NODE_OPTIONS reaches every program the agent runs, so the limit stays out of it", env, wantEnv)
	}
}

func TestLaunch_RefusesAHeapLimitThatIsNotAWholeNumber(t *testing.T) {
	for _, v := range []string{"", "big", "-1", "0", "1024 --inspect"} {
		environ := []string{LaunchVar + "=/data/kiro-cli/node", HeapLimitVar + "=" + v}
		if _, argv, _, err := launch(environ, []string{"/app/marotte"}); err == nil {
			t.Errorf("launch(%s=%q) = %q, nil error; want a refusal", HeapLimitVar, v, argv)
		}
	}
}

func TestLaunch_RefusesAnEmptyNodePath(t *testing.T) {
	if _, _, _, err := launch([]string{LaunchVar + "="}, []string{"/app/marotte"}); err == nil {
		t.Error("launch(empty node) = nil error, want a refusal")
	}
}

func TestEmbeddedNodeHash(t *testing.T) {
	hash := strings.Repeat("0123456789abcdef", 4)
	for _, tc := range []struct {
		name string
		bin  []byte
		want string
	}{
		{"hash right before the name", []byte("\x7fELF..." + hash + NodePathVar + "KIRO_KAS_SERVER_PATH"), hash},
		{"a later occurrence when the first has no hash", []byte(NodePathVar + "...." + hash + NodePathVar), hash},
		// A name straddling the scanner's 1 MiB read boundary is still found.
		{"across a read boundary", append(bytes.Repeat([]byte{0}, 1<<20-40), hash+NodePathVar...), hash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EmbeddedNodeHash(bytes.NewReader(tc.bin))
			if err != nil || got != tc.want {
				t.Errorf("EmbeddedNodeHash = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name string
		bin  []byte
	}{
		{"no name", []byte(hash + "KIRO_KAS_SERVER_PATH")},
		{"uppercase hex", []byte(strings.ToUpper(hash) + NodePathVar)},
		{"too short", []byte(hash[:63] + NodePathVar)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := EmbeddedNodeHash(bytes.NewReader(tc.bin)); !errors.Is(err, errNoNodeHash) {
				t.Errorf("EmbeddedNodeHash = %q, %v; want the no-hash error", got, err)
			}
		})
	}
}

func TestEmbeddedNodePath(t *testing.T) {
	for _, tc := range []struct{ name, dataHome, home, want string }{
		{"XDG_DATA_HOME wins", "/xdg", "/home/u", "/xdg/kiro-cli/node"},
		{"HOME's .local/share", "", "/home/u", "/home/u/.local/share/kiro-cli/node"},
		{"neither", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EmbeddedNodePath(tc.dataHome, tc.home); got != tc.want {
				t.Errorf("EmbeddedNodePath(%q, %q) = %q, want %q", tc.dataHome, tc.home, got, tc.want)
			}
		})
	}
}

func TestFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(path, []byte("node bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("node bytes"))
	if got, err := FileSHA256(path); err != nil || got != hex.EncodeToString(sum[:]) {
		t.Errorf("FileSHA256 = %q, %v; want %x", got, err, sum)
	}
}
