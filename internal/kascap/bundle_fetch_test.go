package kascap

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/pinstall/v3/kirocli"
	"github.com/klauspost/compress/zstd"
)

// The bundle readers skip without a local bundle, which on CI is always. A pull request that changes
// the pin is the one change whose whole point is a new bundle, so there the readers fetch every
// archive whose pin moved instead, and a moved contract turns the PR red rather than skipping.
const (
	// The 2.31 x86_64 zip is about 162 MB, its chat sidecar about 216 MB, the embedded runtime tar
	// about 183 MB and the agent server about 13 MB.
	maxKiroArchive = 1 << 30
	maxChatBinary  = 1 << 30
	maxRuntimeTar  = 1 << 30
	maxAgentServer = 256 << 20
	chatMember     = "kirocli/bin/kiro-cli-chat"
	// Per archive; both together stay under go test's default 10-minute package timeout, which CI
	// does not raise.
	fetchBudget = 4 * time.Minute
)

var pinDigestRe = map[string]*regexp.Regexp{
	"amd64": regexp.MustCompile(`(?m)^KIRO_CLI_SHA256="([0-9a-f]{64})"`),
	"arm64": regexp.MustCompile(`(?m)^KIRO_CLI_SHA256_ARM64="([0-9a-f]{64})"`),
}

// zstdMagic opens every zstd frame (RFC 8878 section 3.1.1).
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// fetched is shared by every bundle reader in the package run, so each archive downloads once.
var fetched struct {
	mu     sync.Mutex
	byArch map[string]fetchResult
}

type fetchResult struct {
	src, url string
	err      error
}

// changedArchesOnThisPR returns the architectures whose kiro-cli archive this GitHub pull request
// changes against its base branch, nil off a pull request. Unreadable base state fails rather than
// skips: skipping is exactly the silent pass the fetch exists to remove. The Go CI checkout has full
// history, so origin/<base> is present.
func changedArchesOnThisPR(t *testing.T) []string {
	t.Helper()
	base := os.Getenv("GITHUB_BASE_REF")
	if base == "" {
		return nil
	}
	cmd := exec.CommandContext(t.Context(), "git", "show", "origin/"+base+":entrypoint.sh")
	cmd.Dir = filepath.Dir(entrypointPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	baseRaw, err := cmd.Output()
	if err != nil {
		t.Fatalf("read the base branch's pin (git show origin/%s:entrypoint.sh): %v: %s", base, err, stderr.String())
	}
	if kiroCLIVersionRe.Find(baseRaw) == nil {
		t.Fatalf("origin/%s:entrypoint.sh carries no KIRO_CLI_VERSION pin", base)
	}
	raw, err := os.ReadFile(entrypointPath)
	if err != nil {
		t.Fatalf("read %s: %v", entrypointPath, err)
	}
	return changedArches(baseRaw, raw)
}

// changedArches returns, sorted, every architecture whose archive differs between the two
// entrypoint.sh contents: all of them on a version change, else each one whose digest moved, as
// Renovate opens a same-version update when one archive is republished.
func changedArches(base, head []byte) []string {
	arches := slices.Sorted(maps.Keys(pinDigestRe))
	if !bytes.Equal(kiroCLIVersionRe.Find(base), kiroCLIVersionRe.Find(head)) {
		return arches
	}
	var changed []string
	for _, arch := range arches {
		if re := pinDigestRe[arch]; !bytes.Equal(re.Find(base), re.Find(head)) {
			changed = append(changed, arch)
		}
	}
	return changed
}

// TestChangedArches pins which entrypoint.sh changes make a pull request fetch which archive: a
// version change fetches every architecture, a digest change only its own, and nothing else fetches.
func TestChangedArches(t *testing.T) {
	const (
		amd = "1111111111111111111111111111111111111111111111111111111111111111"
		arm = "2222222222222222222222222222222222222222222222222222222222222222"
		alt = "3333333333333333333333333333333333333333333333333333333333333333"
	)
	pins := func(version, amd64, arm64, other string) []byte {
		return fmt.Appendf(nil, "KIRO_CLI_VERSION=%q\nKIRO_CLI_SHA256=%q\nKIRO_CLI_SHA256_ARM64=%q\nTINI_VERSION=%q\n",
			version, amd64, arm64, other)
	}
	base := pins("2.31.1", amd, arm, "0.19.0")
	for _, tc := range []struct {
		name string
		head []byte
		want []string
	}{
		{"unchanged", pins("2.31.1", amd, arm, "0.19.0"), nil},
		{"another pin moved", pins("2.31.1", amd, arm, "0.20.0"), nil},
		{"version moved", pins("2.31.2", amd, arm, "0.19.0"), []string{"amd64", "arm64"}},
		{"amd64 digest moved at the same version", pins("2.31.1", alt, arm, "0.19.0"), []string{"amd64"}},
		{"arm64 digest moved at the same version", pins("2.31.1", amd, alt, "0.19.0"), []string{"arm64"}},
		{"both digests moved at the same version", pins("2.31.1", alt, alt, "0.19.0"), []string{"amd64", "arm64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := changedArches(base, tc.head); !slices.Equal(got, tc.want) {
				t.Errorf("changedArches = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPinPatternsMatchTheEntrypoint ties the pin patterns to the real entrypoint.sh: a reshaped
// literal would make changedArches compare two absences and quietly stop fetching that archive.
func TestPinPatternsMatchTheEntrypoint(t *testing.T) {
	raw, err := os.ReadFile(entrypointPath)
	if err != nil {
		t.Fatalf("read %s: %v", entrypointPath, err)
	}
	if n := len(kiroCLIVersionRe.FindAll(raw, -1)); n != 1 {
		t.Errorf("KIRO_CLI_VERSION pattern matches %d lines of %s, want 1", n, entrypointPath)
	}
	for arch, re := range pinDigestRe {
		if n := len(re.FindAll(raw, -1)); n != 1 {
			t.Errorf("%s digest pattern matches %d lines of %s, want 1", arch, n, entrypointPath)
		}
	}
}

// fetchedBundle returns the agent server of the pinned release's arch archive, fetching it on first
// use.
func fetchedBundle(t *testing.T, active, arch string) bundle {
	t.Helper()
	// Read before locking: a Fatalf under the lock would leave it held.
	digest := pinnedDigest(t, arch)
	fetched.mu.Lock()
	res, ok := fetched.byArch[arch]
	if !ok {
		res.src, res.url, res.err = fetchKAS(t.Context(), active, arch, digest)
		if fetched.byArch == nil {
			fetched.byArch = make(map[string]fetchResult)
		}
		fetched.byArch[arch] = res
	}
	fetched.mu.Unlock()
	if res.err != nil {
		t.Fatalf("this pull request changes the kiro-cli %s pin (now %s), so the bundle-backed checks fetch it, and the fetch failed: %v",
			arch, active, res.err)
	}
	return bundle{name: arch, src: res.src, path: res.url + "!/" + chatMember + "!/" + filepath.ToSlash(bundleRel)}
}

func pinnedDigest(t *testing.T, arch string) string {
	t.Helper()
	re, ok := pinDigestRe[arch]
	if !ok {
		t.Fatalf("entrypoint.sh pins no kiro-cli digest for %s", arch)
	}
	raw, err := os.ReadFile(entrypointPath)
	if err != nil {
		t.Fatalf("read %s: %v", entrypointPath, err)
	}
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no %s digest literal in %s", arch, entrypointPath)
	}
	return string(m[1])
}

// fetchKAS downloads the pinned arch archive through the URL marotte installs from, verifies it
// against the pinned digest, and reads the agent server out of its chat sidecar. It returns the
// agent server's source and the archive URL.
func fetchKAS(ctx context.Context, version, arch, digest string) (src, url string, err error) {
	rel := kirocli.Release()
	token, ok := rel.ArchTokens[arch]
	if !ok {
		return "", "", fmt.Errorf("kiro-cli publishes no build for %s", arch)
	}
	url = strings.NewReplacer("{version}", version, "{arch}", token).Replace(rel.URLTemplate)

	dir, err := os.MkdirTemp("", "kascap-kas-*")
	if err != nil {
		return "", url, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	archive := filepath.Join(dir, "kirocli.zip")
	if err := downloadVerified(ctx, url, digest, archive); err != nil {
		return "", url, err
	}
	chat, err := readMember(archive, chatMember)
	if err != nil {
		return "", url, err
	}
	server, err := embeddedAgentServer(chat)
	if err != nil {
		return "", url, fmt.Errorf("%s in %s: %w", chatMember, url, err)
	}
	return string(server), url, nil
}

func downloadVerified(ctx context.Context, url, digest, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, fetchBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: fetchBudget + time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxKiroArchive))
	if err := errors.Join(copyErr, f.Close()); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != digest {
		return fmt.Errorf("%s has sha256 %s, but entrypoint.sh pins %s", url, got, digest)
	}
	return nil
}

func readMember(archive, member string) ([]byte, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	zf, err := zr.Open(member)
	if err != nil {
		return nil, fmt.Errorf("%s in the kiro-cli archive: %w", member, err)
	}
	defer func() { _ = zf.Close() }()
	return io.ReadAll(io.LimitReader(zf, maxChatBinary))
}

// embeddedAgentServer reads the agent server out of the chat sidecar's bytes without running the
// sidecar, so an archive built for another architecture than the runner's reads the same way. The
// sidecar carries its runtime tree as one zstd-compressed tar, which kiro-cli itself unpacks into
// kas/<version>-<sha256 of that frame>/; the frame is the one that decodes to a tar header.
func embeddedAgentServer(chat []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxRuntimeTar))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	var tars []int
	for off := 0; ; off++ {
		i := bytes.Index(chat[off:], zstdMagic)
		if i < 0 {
			break
		}
		off += i
		if opensAsTar(dec, chat[off:]) {
			tars = append(tars, off)
		}
	}
	if len(tars) != 1 {
		return nil, fmt.Errorf("found %d zstd-compressed tars at offsets %v, want the one runtime tree; kiro-cli changed how it embeds the agent server", len(tars), tars)
	}
	if err := dec.Reset(bytes.NewReader(chat[tars[0]:])); err != nil {
		return nil, err
	}
	want := filepath.ToSlash(bundleRel)
	tr := tar.NewReader(dec)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the embedded runtime tree has no %s", want)
		}
		if err != nil {
			return nil, fmt.Errorf("read the embedded runtime tree: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Clean(hdr.Name) != want {
			continue
		}
		server, err := io.ReadAll(io.LimitReader(tr, maxAgentServer+1))
		if err == nil && len(server) > maxAgentServer {
			err = fmt.Errorf("%s exceeds %d bytes", want, maxAgentServer)
		}
		return server, err
	}
}

// TestEmbeddedAgentServer pins the sidecar reader on built bytes: the runtime tar is found among
// other zstd frames and stray magic, and any count of tar frames but one is refused.
func TestEmbeddedAgentServer(t *testing.T) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	defer func() { _ = enc.Close() }()
	tarFrame := func(files ...[2]string) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, f := range files {
			if err := tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg}); err != nil {
				t.Fatalf("tar header: %v", err)
			}
			if _, err := io.WriteString(tw, f[1]); err != nil {
				t.Fatalf("tar body: %v", err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("tar close: %v", err)
		}
		return enc.EncodeAll(buf.Bytes(), nil)
	}
	otherFile := [2]string{"node_modules/other/index.js", "other"}
	runtimeTar := func(server string) []byte {
		return tarFrame(otherFile, [2]string{filepath.ToSlash(bundleRel), server})
	}
	elf := []byte("\x7fELF code")
	stray := append(slices.Clone(zstdMagic), "not a frame"...)
	other := enc.EncodeAll(bytes.Repeat([]byte("x"), 1024), nil)
	sidecar := func(parts ...[]byte) []byte { return bytes.Join(parts, []byte("padding")) }

	t.Run("the one runtime tar among other frames", func(t *testing.T) {
		got, err := embeddedAgentServer(sidecar(elf, stray, other, runtimeTar("server source"), other))
		if err != nil || string(got) != "server source" {
			t.Errorf("embeddedAgentServer = %q, %v; want %q, nil", got, err, "server source")
		}
	})
	for _, tc := range []struct {
		name string
		chat []byte
	}{
		{"no runtime tar", sidecar(elf, stray, other)},
		{"two runtime tars", sidecar(elf, runtimeTar("a"), runtimeTar("b"))},
		{"a runtime tar without the agent server", sidecar(elf, tarFrame(otherFile))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := embeddedAgentServer(tc.chat); err == nil {
				t.Errorf("embeddedAgentServer = %q, nil; want an error", got)
			}
		})
	}
}

// opensAsTar reports whether frame decodes to a tar header block (POSIX ustar magic at byte 257).
func opensAsTar(dec *zstd.Decoder, frame []byte) bool {
	if dec.Reset(bytes.NewReader(frame)) != nil {
		return false
	}
	var block [512]byte
	if _, err := io.ReadFull(dec, block[:]); err != nil {
		return false
	}
	return bytes.HasPrefix(block[257:], []byte("ustar"))
}
