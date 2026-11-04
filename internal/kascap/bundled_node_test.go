package kascap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/kasnode"
	"github.com/klauspost/compress/zstd"
)

// The 2.37.1 x86_64 node is about 125 MB.
const maxNodeBinary = 512 << 20

// nodeReleaseRe is the release URL every official node binary carries (process.release.sourceUrl).
var nodeReleaseRe = regexp.MustCompile(`https://nodejs\.org/download/release/(v[0-9]+\.[0-9]+\.[0-9]+)/node-v`)

// bundledNode is the node a kiro-cli binary embeds: its version and sha256.
type bundledNode struct{ version, sha256 string }

// embeddedNode reads the node out of the kiro-cli binary's bytes without running it: the zstd frame that decodes to an
// ELF whose sha256 is the hash kiro-cli stores before KIRO_KAS_NODE_PATH (kasnode.EmbeddedNodeHash), the hash
// marotte's launcher checks the extracted node against.
func embeddedNode(cli []byte) (bundledNode, error) {
	want, err := kasnode.EmbeddedNodeHash(bytes.NewReader(cli))
	if err != nil {
		return bundledNode{}, fmt.Errorf("no node sha256 before a %s string (%w): kiro-cli moved the hash or stopped reading the variable, so marotte's launcher cannot verify the node or name itself",
			kasnode.NodePathVar, err)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxNodeBinary))
	if err != nil {
		return bundledNode{}, err
	}
	defer dec.Close()
	for off := 0; ; off++ {
		i := bytes.Index(cli[off:], zstdMagic)
		if i < 0 {
			return bundledNode{}, fmt.Errorf("no embedded ELF frame has the sha256 %s stored before %s", want, kasnode.NodePathVar)
		}
		off += i
		node, ok := decodeELF(dec, cli[off:])
		if !ok {
			continue
		}
		sum := sha256.Sum256(node)
		if hex.EncodeToString(sum[:]) != want {
			continue
		}
		m := nodeReleaseRe.FindSubmatch(node)
		if m == nil {
			return bundledNode{}, errors.New("the embedded node carries no nodejs.org release URL, so its version is unknown")
		}
		return bundledNode{version: string(m[1]), sha256: want}, nil
	}
}

// decodeELF returns frame's decoded bytes when they open with the ELF magic.
func decodeELF(dec *zstd.Decoder, frame []byte) ([]byte, bool) {
	if dec.Reset(bytes.NewReader(frame)) != nil {
		return nil, false
	}
	var magic [4]byte
	if _, err := io.ReadFull(dec, magic[:]); err != nil || string(magic[:]) != "\x7fELF" {
		return nil, false
	}
	if dec.Reset(bytes.NewReader(frame)) != nil {
		return nil, false
	}
	// The decoder runs on into the bytes after the frame and fails there, so its error is dropped: the caller's
	// sha256 comparison is what proves the output whole.
	out, _ := io.ReadAll(io.LimitReader(dec, maxNodeBinary+1))
	return out, len(out) <= maxNodeBinary
}

// TestBundledNodeIsValidated fails a pull request that moves the kiro-cli pin onto a release whose bundled node is not
// the one the high throughput flags were validated on, or that no longer reads KIRO_KAS_NODE_PATH. A flag Node drops
// stops the agent server from starting, and a semantic change can alter how async context flows.
func TestBundledNodeIsValidated(t *testing.T) {
	active := activeKASVersion(t)
	arches := changedArchesOnThisPR(t)
	if len(arches) == 0 {
		t.Skip("the bundled node is read from the kiro-cli archive only on a pull request that changes the pin")
	}
	for _, arch := range arches {
		t.Run(arch, func(t *testing.T) {
			res := fetchedArchive(t, active, arch)
			if res.nodeErr != nil {
				t.Fatalf("kiro-cli %s (%s): %v", active, arch, res.nodeErr)
			}
			if res.node.version != kasnode.ValidatedNodeVersion {
				t.Errorf(`kiro-cli %s (%s) bundles Node.js %s, but the "High throughput optimizations" flags (internal/kasnode.Flags) were validated on %s.
Re-run the engine tuning harness on the new Node, then set kasnode.ValidatedNodeVersion to %s.
On Node 24 or later drop --experimental-async-context-frame from kasnode.Flags: it is the default there, and Node 26 refuses it.`,
					active, arch, res.node.version, kasnode.ValidatedNodeVersion, res.node.version)
			}
		})
	}
}

// TestEmbeddedNode pins the binary's node reader on built bytes.
func TestEmbeddedNode(t *testing.T) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	defer func() { _ = enc.Close() }()
	node := []byte("\x7fELF node https://nodejs.org/download/release/v22.23.4/node-v22.23.4.tar.gz")
	other := []byte("\x7fELF bun")
	sum := sha256.Sum256(node)
	hash := hex.EncodeToString(sum[:])
	cli := func(parts ...string) []byte { return []byte(strings.Join(parts, "padding")) }
	frames := []string{"\x7fELF kiro-cli", string(zstdMagic) + "stray", string(enc.EncodeAll(other, nil)), string(enc.EncodeAll(node, nil))}

	t.Run("the frame whose hash kiro-cli stores", func(t *testing.T) {
		got, err := embeddedNode(cli(append(frames, hash+kasnode.NodePathVar+"KIRO_KAS_SERVER_PATH")...))
		if err != nil || got != (bundledNode{version: "v22.23.4", sha256: hash}) {
			t.Errorf("embeddedNode = %+v, %v; want v22.23.4 and its hash", got, err)
		}
	})
	otherSum := sha256.Sum256([]byte("not embedded"))
	for _, tc := range []struct {
		name string
		cli  []byte
	}{
		{"no KIRO_KAS_NODE_PATH", cli(append(frames, hash+"KIRO_KAS_SERVER_PATH")...)},
		{"a hash no frame has", cli(append(frames, hex.EncodeToString(otherSum[:])+kasnode.NodePathVar)...)},
		{"a node with no release URL", cli(string(enc.EncodeAll(other, nil)), func() string {
			s := sha256.Sum256(other)
			return hex.EncodeToString(s[:]) + kasnode.NodePathVar
		}())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := embeddedNode(tc.cli); err == nil {
				t.Errorf("embeddedNode = %+v, nil; want an error", got)
			}
		})
	}
}
