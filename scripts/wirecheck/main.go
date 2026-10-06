// Command wirecheck asserts wire-protocol compatibility between the Go server half (the
// web-terminal-engine module go.mod pins) and the bundled TS client half (the npm artifact
// static-src/shell.ts imports). The two are pinned independently, so this gate is the
// only proof the shipped pair is compatible; otherwise a mismatch deploys healthy and
// breaks only the shell tab (close 4002). The rule is the engine's
// terminal.WirePairIncompatibility, the same verdict as its runtime handshake.
//
// Exit 0: compatible. Exit 1: a declared floor is violated. Exit 2: usage error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/cplieger/web-terminal-engine/v6/terminal"
)

// readManifest resolves the client half from the engine artifact's published manifest
// via the engine's decoder (scraping the vendored TS breaks on any reformat). Every
// failure is exit 2: an unreadable manifest means the gate is broken. An unknown schema
// is named separately: its remedy is to bump this gate.
func readManifest(path string, stderr io.Writer) (clientRev, clientMinServer int, ok bool) {
	m, err := terminal.ReadWireManifest(path)
	if err != nil {
		if errors.Is(err, terminal.ErrWireManifestSchema) {
			fmt.Fprintf(stderr, "wirecheck: %s: the manifest format moved ahead of this gate (fix the gate, do not bump a pin): %v\n", path, err)
		} else {
			fmt.Fprintf(stderr, "wirecheck: cannot read the engine's wire-compatibility manifest at %s (fix the gate, do not bump a pin): %v\n", path, err)
		}
		return 0, 0, false
	}
	return m.ProtocolVersion, m.MinimumServerProtocolVersion, true
}

func main() {
	manifest := flag.String("manifest", "", "path to the vendored engine artifact's wire-compatibility.json (preferred over the -client-* flags)")
	clientRev := flag.Int("client-rev", 0, "client WIRE_PROTOCOL_VERSION from the vendored npm artifact")
	clientMinServer := flag.Int("client-min-server", 0, "client MIN_SUPPORTED_SERVER_WIRE_VERSION from the vendored npm artifact")
	flag.Parse()
	rev, minServer := *clientRev, *clientMinServer
	if *manifest != "" {
		var ok bool
		if rev, minServer, ok = readManifest(*manifest, os.Stderr); !ok {
			os.Exit(2)
		}
	}
	os.Exit(run(rev, minServer, os.Stdout, os.Stderr))
}

// run performs the wire-floor gate and returns the exit code (0 compatible, 1 floor
// violated, 2 usage error). Flags are validated here so a missing extraction reports as a
// usage error rather than a compatibility verdict.
func run(clientRev, clientMinServer int, stdout, stderr io.Writer) int {
	if clientRev <= 0 || clientMinServer <= 0 {
		fmt.Fprintln(stderr, "wirecheck: -client-rev and -client-min-server are required positive integers")
		return 2
	}
	if reason := terminal.WirePairIncompatibility(terminal.WirePair{
		Server: terminal.WireEnd{
			Rev:     terminal.WireProtocolVersion,
			MinPeer: terminal.MinSupportedClientWireVersion,
		},
		Client: terminal.WireEnd{Rev: clientRev, MinPeer: clientMinServer},
	}); reason != "" {
		fmt.Fprintf(stderr, "ERROR wire-floor-mismatch: %s\n%s\n", reason, remediation())
		return 1
	}
	fmt.Fprintf(stdout, "wirecheck ok: server wire rev %d (min client %d) <-> client wire rev %d (min server %d)\n",
		terminal.WireProtocolVersion, terminal.MinSupportedClientWireVersion, clientRev, clientMinServer)
	return 0
}

// remediation names this repo's engine pins: which pin to move is build-layout knowledge
// the engine does not carry.
func remediation() string {
	return "fix: bump go.mod's web-terminal-engine (Go half) or the Dockerfile's CPLIEGER_WEB_TERMINAL_ENGINE_VERSION ARG + static-src/package.json pin (TS half) so both halves resolve to a compatible pair"
}
