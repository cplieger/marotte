package filebrowse

import (
	"strings"
	"testing"
)

// FuzzEnforce pins the access-control contract across arbitrary paths: enforce denies exactly what
// the policy blocks, anything outside the granted mounts or on a sensitive path.
func FuzzEnforce(f *testing.F) {
	f.Add("/workspace/file.txt")
	f.Add("/workspace")
	f.Add("/etc/passwd")
	f.Add("/config/chats/a.json")
	f.Add("/config/kiro/steering/marotte.md")
	f.Add("/config")
	f.Add("/configextra/x")
	f.Add("/../etc/shadow")
	f.Add("/app/../workspace")
	f.Add("/\x00etc")
	f.Add("/CONFIG/CHATS/x")
	f.Add("")
	f.Add("/")

	h := &Handler{mounts: []mount{
		{dir: "/workspace", name: "workspace"},
		{dir: "/config", name: "config"},
	}}

	f.Fuzz(func(t *testing.T, path string) {
		m, err := h.enforce(path)

		granted := h.mountFor(path) != nil
		blocked := !granted || h.sensitive.Blocks(path)

		if blocked && err == nil {
			t.Fatalf("enforce(%q) = nil, want denial (granted=%v sensitive=%v)",
				path, granted, h.sensitive.Blocks(path))
		}
		if !blocked && err != nil {
			t.Fatalf("enforce(%q) = %v, want allow (granted and not sensitive)", path, err)
		}
		if err == nil && m != h.mountFor(path) {
			t.Fatalf("enforce(%q) returned mount %v, want %v", path, m, h.mountFor(path))
		}
	})
}

// FuzzIsProtectedDir pins that the protected-directory verdict does not depend on how many trailing
// slashes the caller passes.
func FuzzIsProtectedDir(f *testing.F) {
	f.Add("/config")
	f.Add("/config/chats")
	f.Add("/config/chats/")
	f.Add("/workspace")
	f.Add("/config/kiro/agents")
	f.Add("/")
	f.Add("")
	f.Add("/config/chats///")

	f.Fuzz(func(t *testing.T, path string) {
		got := (Sensitive{}).protectedDir(path)
		if trimmed := (Sensitive{}).protectedDir(strings.TrimRight(path, "/")); trimmed != got {
			t.Fatalf("protectedDir(%q)=%v but trailing-slash-trimmed form=%v", path, got, trimmed)
		}
		if slashed := (Sensitive{}).protectedDir(path + "/"); slashed != got {
			t.Fatalf("protectedDir(%q)=%v but trailing-slash-added form=%v", path, got, slashed)
		}
	})
}
