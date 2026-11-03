package kascap

import (
	"bytes"
	"strings"
	"testing"
)

// marotteClientName is the clientInfo.name marotte sends on initialize (internal/bridge/engine.go).
const marotteClientName = "marotte"

// The literals kiro-cli's acp engine resolver carries: the header of its embedded telemetry snapshot of ACP clients
// kept on v2 when no --agent-engine is passed, and the resolver's own tracing message.
var (
	acpSnapshotHeader  = []byte("# Snapshot of ACP clients seen in telemetry")
	acpResolverMessage = []byte("resolved kiro-cli acp engine")
)

// acpFallback is what a kiro-cli binary says about `kiro-cli acp` with no --agent-engine: whether it still keeps
// some clients on v2 (present), and the client names it lists for that.
type acpFallback struct {
	clients []string
	present bool
}

// readACPFallback reads the snapshot out of a kiro-cli binary: comment lines, then one client name per line, ending
// at the first line that is neither.
func readACPFallback(cli []byte) acpFallback {
	at := bytes.Index(cli, acpSnapshotHeader)
	if at < 0 || !bytes.Contains(cli, acpResolverMessage) {
		return acpFallback{}
	}
	fb := acpFallback{present: true}
	rest := cli[at:]
	for {
		line, tail, ok := bytes.Cut(rest, []byte("\n"))
		if !ok {
			return fb
		}
		switch {
		case bytes.HasPrefix(line, []byte("#")):
		case isClientName(line):
			fb.clients = append(fb.clients, string(line))
		default:
			return fb
		}
		rest = tail
	}
}

func isClientName(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	for _, c := range line {
		if c <= ' ' || c > '~' || c == '#' {
			return false
		}
	}
	return true
}

// listsClient matches name the way the snapshot's own header says: case-insensitively, a trailing `*` matching a
// prefix.
func listsClient(clients []string, name string) bool {
	name = strings.ToLower(name)
	for _, c := range clients {
		c = strings.ToLower(c)
		if prefix, ok := strings.CutSuffix(c, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if c == name {
			return true
		}
	}
	return false
}

// TestACPEngineFlagStillNeeded fails a pull request that moves the kiro-cli pin onto a release where `kiro-cli acp`
// keeps no client on v2 by default, so --agent-engine v3 has become redundant, and one that lists marotte for the v2
// fallback, which makes the flag load-bearing.
func TestACPEngineFlagStillNeeded(t *testing.T) {
	active := activeKASVersion(t)
	arches := changedArchesOnThisPR(t)
	if len(arches) == 0 {
		t.Skip("the acp engine fallback is read from the kiro-cli archive only on a pull request that changes the pin")
	}
	for _, arch := range arches {
		t.Run(arch, func(t *testing.T) {
			fb := fetchedArchive(t, active, arch).acp
			if !fb.present {
				t.Fatalf("kiro-cli %s (%s): kiro-cli acp now starts v3 for every client; remove --agent-engine v3 from buildACPArgs (internal/bridge/engine_process.go) and this test.",
					active, arch)
			}
			if len(fb.clients) == 0 {
				t.Fatalf("kiro-cli %s (%s): the ACP client snapshot after %q lists no client; re-read its layout in the binary before trusting this check",
					active, arch, acpSnapshotHeader)
			}
			if listsClient(fb.clients, marotteClientName) {
				t.Errorf("kiro-cli %s (%s): %s is listed for the v2 fallback; --agent-engine v3 is now load-bearing, keep it.",
					active, arch, marotteClientName)
			}
		})
	}
}

// TestReadACPFallback pins the snapshot reader and matcher on built bytes shaped like 3.0.0's.
func TestReadACPFallback(t *testing.T) {
	const resolver = "event crates/chat-cli/src/cli/acp_engine.rs:95resolved kiro-cli acp engine"
	snapshot := func(names ...string) string {
		return "SSH_TTY# Clients from acp_clients_snapshot.txt that follow the default\nzed\n" +
			string(acpSnapshotHeader) + ": the acp_client_name dimension\n# a trailing `*` matches a prefix.\n" +
			strings.Join(names, "\n") + "\nzoom-scribe\n" + resolver
	}
	for _, tc := range []struct {
		name    string
		cli     string
		present bool
		listed  bool
	}{
		{"marotte absent", snapshot("quorum_daedalus_*", "kirocrew", "acp-client"), true, false},
		{"marotte listed", snapshot("kirocrew", "marotte", "acp-client"), true, true},
		{"marotte listed in another case", snapshot("Marotte"), true, true},
		{"marotte matched by a prefix", snapshot("maro*"), true, true},
		{"a longer name is not marotte", snapshot("marotte-probe"), true, false},
		{"no snapshot", "SSH_TTY" + resolver, false, false},
		{"no resolver", strings.TrimSuffix(snapshot("kirocrew"), resolver), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb := readACPFallback([]byte(tc.cli))
			if fb.present != tc.present {
				t.Errorf("present = %v, want %v", fb.present, tc.present)
			}
			if got := listsClient(fb.clients, marotteClientName); got != tc.listed {
				t.Errorf("listsClient(%q, marotte) = %v, want %v", fb.clients, got, tc.listed)
			}
		})
	}
	t.Run("the list ends at the first line that is not a name", func(t *testing.T) {
		fb := readACPFallback([]byte(snapshot("kirocrew")))
		if want := []string{"kirocrew", "zoom-scribe"}; strings.Join(fb.clients, ",") != strings.Join(want, ",") {
			t.Errorf("clients = %q, want %q", fb.clients, want)
		}
	})
}
