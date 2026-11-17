package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/testsupport/testchild"
	"github.com/creack/pty"
)

// xclipCommand runs bin as the image's xclip over env, in a new session so it has
// no controlling terminal unless the caller gives it one. A binary that misses the
// xclip branch boots the server, so its folders and listener point at scratch.
func xclipCommand(t *testing.T, env []string, bin, stdin string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(env,
		"LISTEN_ADDR=127.0.0.1:0",
		"KIRO_CONFIG_DIR="+filepath.Join(dir, "config"),
		"KIRO_WORK_DIR="+filepath.Join(dir, "workspace"),
		"MAROTTE_TOOLS_DIR="+filepath.Join(dir, "tools"),
		"HOME="+dir,
		"KIRO_HOME="+filepath.Join(dir, ".kiro"),
		"MAROTTE_BUNDLED_TOOLS="+filepath.Join(dir, "absent.json"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = strings.NewReader(stdin)
	return cmd
}

// TestXclipDispatch: run as xclip, the binary is osc52copy's stand-in, whose
// Main returns 1 for a failed copy or a paste, with one line on stderr.
func TestXclipDispatch(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		args       []string
		code       int
		mentionTTY bool
	}{
		{"copy with no terminal fails at the tty open", []string{"-selection", "clipboard"}, 1, true},
		{"paste is refused before the terminal is opened", []string{"-o"}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := xclipCommand(t, testchild.Env(), self, "copied text", tc.args...)
			cmd.Args[0] = filepath.Join(t.TempDir(), "xclip")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			code := 0
			var exit *exec.ExitError
			if err := cmd.Run(); errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatalf("run as xclip: %v", err)
			}
			if code != tc.code {
				t.Errorf("xclip %v exited %d, want %d; stderr %q", tc.args, code, tc.code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("xclip %v wrote %q to stdout, want nothing", tc.args, stdout.String())
			}
			if got := stderr.String(); !strings.HasPrefix(got, "xclip: ") || strings.Count(got, "\n") != 1 {
				t.Errorf("xclip %v stderr = %q, want one line starting \"xclip: \"", tc.args, got)
			}
			if got := strings.Contains(stderr.String(), "/dev/tty"); got != tc.mentionTTY {
				t.Errorf("xclip %v stderr = %q; mentions /dev/tty = %v, want %v", tc.args, stderr.String(), got, tc.mentionTTY)
			}
		})
	}
}

// TestXclipLinkWritesOneOSC52Sequence runs the built binary through a link
// named xclip, as kiro-cli does in the Shell panel. With the pty buffer full
// and another writer on the terminal, a split write lets that writer's bytes
// land inside the sequence, which cuts the payload the browser decodes.
func TestXclipLinkWritesOneOSC52Sequence(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "marotte")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	link := filepath.Join(dir, "xclip")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("full-screen drag selection\n", 12_000)
	cmd := xclipCommand(t, os.Environ(), link, text, "-selection", "clipboard")
	cmd.ExtraFiles = []*os.File{slave}
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 3 // ExtraFiles[0] in the child
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	noiseDone := make(chan struct{})
	go func() {
		defer close(noiseDone)
		for {
			if _, err := slave.Write([]byte{'#'}); err != nil {
				return // the master closed
			}
		}
	}()
	// The master closes first: a write blocked on the slave ends only then.
	t.Cleanup(func() {
		_ = master.Close()
		<-noiseDone
		_ = slave.Close()
	})

	const prefix, terminator = "\x1b]52;c;", "\x1b\\"
	if err := master.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var out []byte
	buf := make([]byte, 1024)
	start, end := -1, -1
	for end < 0 {
		n, err := master.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("read the pty master: %v; xclip stderr %q", err, stderr.String())
		}
		if start < 0 {
			start = bytes.Index(out, []byte(prefix))
		}
		if start >= 0 {
			end = bytes.Index(out[start+len(prefix):], []byte(terminator))
		}
		time.Sleep(50 * time.Microsecond) // a slow reader keeps the pty buffer full
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("xclip copy: %v; stderr %q", err, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("xclip copy wrote stdout %q, stderr %q; want both empty", stdout.String(), stderr.String())
	}

	payload := out[start+len(prefix) : start+len(prefix)+end]
	if foreign := bytes.Count(payload, []byte{'#'}); foreign > 0 {
		t.Fatalf("%d foreign bytes inside the %d-byte OSC 52 payload", foreign, len(payload))
	}
	got, err := base64.StdEncoding.DecodeString(string(payload))
	if err != nil {
		t.Fatalf("decode the OSC 52 payload: %v", err)
	}
	if string(got) != text {
		t.Errorf("OSC 52 payload decodes to %d bytes, want the %d-byte input", len(got), len(text))
	}
}

// TestDockerfileLinksXclip: kiro-cli copies by running xclip, so the runtime
// image links that name to the server binary.
func TestDockerfileLinksXclip(t *testing.T) {
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	logical := strings.ReplaceAll(string(raw), "\\\n", " ")
	runtime := logical[strings.LastIndex(logical, "\nFROM "):]
	for line := range strings.Lines(runtime) {
		if strings.HasPrefix(line, "RUN ") && strings.Contains(line, "ln -s /app/marotte /usr/bin/xclip") {
			return
		}
	}
	t.Error("no RUN in the Dockerfile's runtime stage links /app/marotte to /usr/bin/xclip")
}
