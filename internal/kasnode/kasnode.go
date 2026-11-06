// Package kasnode runs kiro-cli's agent server on its own bundled Node.js with the high throughput flags. kiro-cli
// starts that node from KIRO_KAS_NODE_PATH when it is set, so marotte points the variable at its own binary, which
// re-executes the real node with Flags ahead of kiro-cli's arguments (Exec).
package kasnode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

const (
	// NodePathVar is read by kiro-cli (undocumented; it sits beside KIRO_KAS_SERVER_PATH in the binary) and names
	// the node it runs the agent server on. While it is set, kiro-cli does not extract its embedded node.
	NodePathVar = "KIRO_KAS_NODE_PATH"
	// LaunchVar carries the real node's path to the re-executed marotte binary; its presence selects Exec.
	LaunchVar = "MAROTTE_KAS_NODE"
	// HeapLimitVar carries the JavaScript heap limit in MiB to the launcher, which puts it on node's argv.
	HeapLimitVar = "MAROTTE_KAS_NODE_HEAP_MIB"
)

// ValidatedNodeVersion is the Node.js Flags were measured on, the one kiro-cli 2.37.1 and 3.0.0 bundle. A kiro-cli bump that
// bundles another fails internal/kascap's TestBundledNodeIsValidated; re-run the tuning harness on the new Node before
// changing it.
const ValidatedNodeVersion = "v22.23.4"

// Flags go on node's argv, never in NODE_OPTIONS: NODE_OPTIONS reaches every program the agent runs, and Node 24
// and later refuse --experimental-async-context-frame there. Validated on Node v22.23.4 only.
var Flags = []string{"--experimental-async-context-frame", "--max-semi-space-size=64"}

// Exec replaces the process with the node LaunchVar names, Flags, then args. It returns only on failure.
func Exec(environ, args []string) error {
	node, argv, env, err := launch(environ, args)
	if err != nil {
		return err
	}
	return syscall.Exec(node, argv, env)
}

// HeapLimitArg is the node flag that sets the JavaScript heap limit to mib MiB.
func HeapLimitArg(mib int) string {
	return "--max-old-space-size=" + strconv.Itoa(mib)
}

// launch builds Exec's call. Every variable leaves the environment, so nothing the agent server spawns (a nested
// kiro-cli in a shell command) starts marotte as its node or inherits the engine's heap limit.
func launch(environ, args []string) (node string, argv, env []string, err error) {
	heap, heapSet := "", false
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, LaunchVar+"="); ok {
			node = v
		}
		if v, ok := strings.CutPrefix(kv, HeapLimitVar+"="); ok {
			heap, heapSet = v, true
		}
	}
	if node == "" {
		return "", nil, nil, fmt.Errorf("%s is empty: no node to run", LaunchVar)
	}
	argv = append([]string{node}, Flags...)
	if heapSet {
		mib, convErr := strconv.Atoi(heap)
		if convErr != nil || mib <= 0 || strings.Trim(heap, "0123456789") != "" {
			return "", nil, nil, fmt.Errorf("%s=%q is not a heap limit in MiB", HeapLimitVar, heap)
		}
		argv = append(argv, HeapLimitArg(mib))
	}
	env = slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		return strings.HasPrefix(kv, LaunchVar+"=") || strings.HasPrefix(kv, NodePathVar+"=") ||
			strings.HasPrefix(kv, HeapLimitVar+"=")
	})
	if len(args) > 1 {
		argv = append(argv, args[1:]...)
	}
	return node, argv, env, nil
}

// errNoNodeHash means the binary does not contain the variable name with a hash before it.
var errNoNodeHash = errors.New("no KIRO_KAS_NODE_PATH string preceded by a node sha256")

// EmbeddedNodeHash returns the sha256 of the node kiro-cli embeds: in 3.0.0 its 64 lowercase hex digits sit
// immediately before the first NodePathVar string. An absence is errNoNodeHash.
func EmbeddedNodeHash(r io.Reader) (string, error) {
	const chunk = 1 << 20
	needle := []byte(NodePathVar)
	keep := len(needle) + sha256.Size*2
	var window []byte
	buf := make([]byte, chunk)
	for {
		n, readErr := r.Read(buf)
		window = append(window, buf[:n]...)
		if h, ok := hashBefore(window, needle); ok {
			return h, nil
		}
		if errors.Is(readErr, io.EOF) {
			return "", errNoNodeHash
		}
		if readErr != nil {
			return "", readErr
		}
		if len(window) > keep {
			window = append(window[:0], window[len(window)-keep:]...)
		}
	}
}

// hashBefore returns the first 64 lowercase hex digits that immediately precede needle in window.
func hashBefore(window, needle []byte) (string, bool) {
	const width = sha256.Size * 2
	for off := 0; ; {
		i := bytes.Index(window[off:], needle)
		if i < 0 {
			return "", false
		}
		at := off + i
		if at >= width && isLowerHex(window[at-width:at]) {
			return string(window[at-width : at]), true
		}
		off = at + 1
	}
}

func isLowerHex(b []byte) bool {
	for _, c := range b {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// EmbeddedNodePath is where kiro-cli extracts the node it runs the agent server on: its data dir is
// $XDG_DATA_HOME/kiro-cli, else $HOME/.local/share/kiro-cli. "" when neither is set. The node's sha256 is written
// beside it, in node.sha256.
func EmbeddedNodePath(dataHome, home string) string {
	if dataHome == "" {
		if home == "" {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "kiro-cli", "node")
}

// FileSHA256 returns the lowercase hex sha256 of the file at path.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
