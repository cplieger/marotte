package steering

import (
	"strings"
	"testing"
)

// fakeEnv substitutes lookupEnv for the test's life. Unset names answer
// (_, false), so a test can pin absence as well as presence.
func fakeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := lookupEnv
	lookupEnv = func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
	t.Cleanup(func() { lookupEnv = prev })
}

func TestWriteRuntime_PrintsTheLiveEnv(t *testing.T) {
	fakeEnv(t, map[string]string{
		"HOME":      "/h",
		"KIRO_HOME": "/h/.kiro",
		"PATH":      "/a:/b",
		"GOPATH":    "/g",
		"GOBIN":     "/g/bin",
		"LANG":      "C.UTF-8",
	})
	var b strings.Builder
	writeRuntime(&b, "/cfg")
	out := b.String()

	for _, want := range []string{
		"## Container runtime",
		"`HOME=/h`",
		"`KIRO_HOME=/h/.kiro`",
		"`PATH=/a:/b`",
		"`GOPATH=/g`, `GOBIN=/g/bin`, `LANG=C.UTF-8`",
		"`/cfg` is the persistent volume",
		"`/cfg/tools/bin/<tool>`",
		"`HOME=/cfg/home`",
		"/etc/profile.d/10-marotte-path.sh",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeRuntime output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TZ=") {
		t.Errorf("writeRuntime printed TZ, which was unset:\n%s", out)
	}
}

// A value carrying a backtick would close the code span it is quoted in, so
// the variable is dropped rather than rendered.
func TestWriteRuntime_BacktickInValueDropsTheVariable(t *testing.T) {
	fakeEnv(t, map[string]string{
		"HOME":   "/h",
		"PATH":   "/a:/b",
		"GOPATH": "/g`\n## Capabilities",
		"LANG":   "C.UTF-8",
	})
	var b strings.Builder
	writeRuntime(&b, "/cfg")
	out := b.String()
	if strings.Contains(out, "GOPATH=") {
		t.Errorf("a GOPATH value with a backtick reached the doc:\n%s", out)
	}
	if !strings.Contains(out, "`LANG=C.UTF-8`") {
		t.Errorf("dropping GOPATH also dropped its clean siblings:\n%s", out)
	}
	if strings.Contains(out, "\n## Capabilities") {
		t.Errorf("a newline in an env value produced a heading:\n%s", out)
	}
}

func TestWriteToolsEngine(t *testing.T) {
	var b strings.Builder
	writeToolsEngine(&b, "/cfg")
	out := b.String()
	for _, want := range []string{
		"localhost:9847/api/tools/search?q=<name>",
		"POST localhost:9847/api/tools",
		`"apt":true`,
		"&unavailable=1",
		"Settings → Tools",
		"`/cfg/tools.json`",
		"`/cfg/tools/opt/<name>/<version>/`",
		`"Refresh catalog"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeToolsEngine output missing %q:\n%s", want, out)
		}
	}
	// The catalog count moves with every refresh; a printed number goes stale.
	if strings.Contains(out, "~870") {
		t.Errorf("writeToolsEngine printed the retired catalog count:\n%s", out)
	}
	// static/index.html's button text is "Refresh catalog"; the longer string is
	// its aria-label, which no user sees.
	if strings.Contains(out, `"Refresh the tool catalog"`) {
		t.Errorf("writeToolsEngine quoted an aria-label as the on-screen label:\n%s", out)
	}
}

func TestWriteGitPanel(t *testing.T) {
	var b strings.Builder
	writeGitPanel(&b, "/w", true)
	out := b.String()
	for _, want := range []string{
		"## Git panel",
		"**Changes**",
		"**Pull requests**",
		"**Sources**",
		"git push -u origin <branch>",
		"--ff-only",
		"$HOME/.gitconfig",
		"`/w/<name>`",
		"against an `https://` remote is authenticated",
		`"Check"`,
		`"Reconnect"`,
		`"Sign out"`,
		`"No other owners"`,
		"open PRs of the account's own repositories and its other owners'",
		`"Contributions elsewhere"`,
		`"Undo"`,
		`"Load more repositories"`,
		`"Sign in with GitLab"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeGitPanel(connected) output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "GitLab, Gitea and Codeberg by token") {
		t.Errorf("writeGitPanel(connected) says GitLab connects by token only:\n%s", out)
	}
}

// With no forge connected, the closing paragraph points at Sources instead of
// describing an account that does not exist.
func TestWriteGitPanel_NoForge(t *testing.T) {
	var b strings.Builder
	writeGitPanel(&b, "/w", false)
	out := b.String()
	if !strings.Contains(out, `Sources → "Add an account"`) {
		t.Errorf("writeGitPanel(none) does not point the user at Sources:\n%s", out)
	}
	if strings.Contains(out, "the account the Sources tab holds") {
		t.Errorf("writeGitPanel(none) describes a connected account:\n%s", out)
	}
}

// TestStaticGuide_NamesMarottesHelperNotACLILogin pins how the static sections
// say git is authenticated: connecting in Sources registers Marotte's own
// helper, and no forge CLI login or tool path is offered.
func TestStaticGuide_NamesMarottesHelperNotACLILogin(t *testing.T) {
	var b strings.Builder
	writeGitPanel(&b, "/w", true)
	out := b.String()
	if !strings.Contains(out, "registers marotte's git credential helper") {
		t.Errorf("writeGitPanel(connected) does not name Marotte's git credential helper:\n%s", out)
	}
	if cli := forgeCLIName.FindString(out); cli != "" {
		t.Errorf("writeGitPanel(connected) names the forge CLI %q:\n%s", cli, out)
	}
	if strings.Contains(out, "CLI login") {
		t.Errorf("writeGitPanel(connected) describes a forge connect as a CLI login:\n%s", out)
	}

	b.Reset()
	writeGitPanel(&b, "/w", false)
	if out := b.String(); !strings.Contains(out, "registers marotte's git credential helper") {
		t.Errorf("writeGitPanel(none) does not name Marotte's git credential helper:\n%s", out)
	}

	fakeEnv(t, map[string]string{"HOME": "/h", "PATH": "/a:/b"})
	b.Reset()
	writeRuntime(&b, "/cfg")
	if cli := forgeCLIName.FindString(b.String()); cli != "" {
		t.Errorf("writeRuntime names the forge CLI %q:\n%s", cli, b.String())
	}
}

// The notifications path is the one a user asks about most; the labels must be
// the exact on-screen text.
func TestWriteUIGuide_NotificationsPath(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b)
	out := b.String()
	for _, want := range []string{
		"Settings → General",
		`"Push notifications"`,
		`"Agent finished"`,
		`"Pull request checks"`,
		`"Workflow runs"`,
		"has no switch of its own",
		// The one off-by-default kind, named with its reason: "why am I not getting
		// pull-request notifications" is otherwise answerable only from source.
		`"Pull request checks" starts OFF`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeUIGuide output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteUIGuide_NamesEveryTab(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b)
	out := b.String()
	for _, want := range []string{
		`"General", "Tools", "Permissions", "Custom instructions"`,
		`"Toggle git"`,
		`"Global instructions"`,
		"`/docs`",
		"`/history`",
		"`/files`",
		"`/run/<id>`",
		`(tooltip "Cancel this turn")`,
		"history, web.",
		"`/web/<path>`",
		"(Fill, Phone, Tablet, Desktop)",
		`"Scale to fit"`,
		`"Reload preview"`,
		`"Open preview"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeUIGuide output missing %q:\n%s", want, out)
		}
	}
	// untrusted_test.go counts exactly one such heading in the whole doc.
	if strings.Contains(out, "\n## Capabilities") {
		t.Errorf("writeUIGuide emitted a second Capabilities heading:\n%s", out)
	}
}

func TestWriteAttachments_UsesTheUploadDirConstant(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/u", "/w")
	out := b.String()
	for _, want := range []string{
		"`/u/pasted-YYYY-MM-DDTHH-MM-SS.png`",
		"`/u/paste-YYYY-MM-DDTHH-MM-SS.txt`",
		"`![label](/w/out/shot.png)`",
		"under `/w/` or `/u/`",
		"`ls -t /u | head`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
	for _, retired := range []string{"/workspace/uploads", "`/uploads/`"} {
		if strings.Contains(out, retired) {
			t.Errorf("writeAttachments hardcoded %q instead of the upload dir parameter:\n%s", retired, out)
		}
	}
}

func TestWriteAttachments_TellsHowToLinkAFile(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/u", "/w")
	out := b.String()
	for _, want := range []string{
		"`[label](/w/path/file.ext#L<line>)`",
		"opens the file in a marotte editor tab",
		"a bare absolute path outside backticks becomes a button that opens the file",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteAttachments_StatesTheCaps(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/u", "/w")
	out := b.String()
	for _, want := range []string{
		"10 MiB",
		"5 MiB base64",
		"15 MiB per prompt",
		"16 images",
		"2000px",
		"255 MiB per gesture (the total across every file in one upload), 25 files",
		"the stamp is UTC",
		"NO filename",
		"png/jpg/jpeg/gif/webp/svg/avif/ico/bmp",
		"mp3/wav/ogg/m4a/flac/aac/opus",
		"`.svg` and `.avif` are never inlined",
		"The request was refused as sent",
		"pixel dimensions or image count over its limit",
		"unsupported or mismatched format",
		"does not check whether the model takes images",
		"only a PASTED image is downscaled",
		"multi-line TEXT over 50 lines or 10,000 characters",
		"a `resource` block whose `uri` is the file path",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
	// validationGuidance (internal/command/prompt.go) fires only on the
	// backend's payload-validation names, none of which describes a model
	// capability, so a refusal for that cause never carries this sentence.
	for _, retired := range []string{"model without vision", "embedded the same way"} {
		if strings.Contains(out, retired) {
			t.Errorf("writeAttachments carries the retired phrase %q:\n%s", retired, out)
		}
	}
}

// The Limitations bullets are pinned byte-for-byte.
func TestWriteLimitations_Verbatim(t *testing.T) {
	var b strings.Builder
	writeLimitations(&b)
	want := "## Limitations\n\n" +
		"- Shell commands run in the container, not on the host\n" +
		"- No GUI or browser; web_fetch and web_search work\n" +
		"- Container has no Docker socket\n\n"
	if got := b.String(); got != want {
		t.Errorf("writeLimitations() = %q, want %q", got, want)
	}
}

func TestWriteCapabilities(t *testing.T) {
	var b strings.Builder
	writeCapabilities(&b, "/cfg")
	out := b.String()
	for _, want := range []string{
		"`/cfg/chats/<id>/entries.jsonl`",
		"Undo is per TURN, not per file",
		"no resume or retry tool",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeCapabilities output missing %q:\n%s", want, out)
		}
	}
	// The retired claim: marotte captures nothing, KAS snapshots only its own
	// edit tools, and the one user affordance is Rewind.
	if strings.Contains(out, "checkpointed server-side") {
		t.Errorf("writeCapabilities repeated the retired per-turn checkpoint claim:\n%s", out)
	}
}

func TestWriteAttachments_TeachesTheHTMLPreview(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/workspace/.uploads", "/workspace")
	out := b.String()
	for _, want := range []string{
		"`[Demo](/workspace/demo/index.html)`",
		"its own folder under `/workspace`",
		"is refused",
		"starting with `.`",
		"--base=./`",
		`<meta name="marotte-preview" content="phone">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
}
