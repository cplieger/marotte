package steering

import (
	"strings"
	"testing"
)

// Unset names answer (_, false), so a test can pin absence as well as presence.
func fakeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := lookupEnv
	lookupEnv = func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
	t.Cleanup(func() { lookupEnv = prev })
}

func fakeUID(t *testing.T, uid int) {
	t.Helper()
	prev := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = prev })
}

func TestWriteRuntime_PrintsTheLiveEnv(t *testing.T) {
	fakeEnv(t, map[string]string{
		"HOME":      "/h",
		"KIRO_HOME": "/h/.kiro",
		"PATH":      "/t/bin:/t/go/bin:/a",
		"GOPATH":    "/g",
		"GOBIN":     "/g/bin",
		"LANG":      "C.UTF-8",

		"MAROTTE_PATH_DROPIN": "1",
	})
	fakeUID(t, 0)
	var b strings.Builder
	writeRuntime(&b, "/cfg", "/t")
	out := b.String()

	for _, want := range []string{
		"## Container runtime",
		"`HOME=/h`",
		"`KIRO_HOME=/h/.kiro`",
		"`PATH=/t/bin:/t/go/bin:/a`: `/t/bin` is the tools engine's single bin dir",
		"`GOPATH=/g`, `GOBIN=/g/bin`, `LANG=C.UTF-8`",
		"`/cfg` is the persistent volume",
		"`/t` is the tools engine's tree",
		"`/t/bin` is the tools engine's single bin dir",
		"`/t/bin/<tool>`",
		"Pin `HOME=/h` on any command",
		"sources `/etc/profile.d/10-marotte-path.sh`, which holds this start's PATH, so an installed tool resolves by bare name there too",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeRuntime output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TZ=") {
		t.Errorf("writeRuntime printed TZ, which was unset:\n%s", out)
	}
	if strings.Contains(out, "/cfg/tools") || strings.Contains(out, "/cfg/home") {
		t.Errorf("writeRuntime derived a path from the config dir instead of the tools dir or HOME:\n%s", out)
	}
	if strings.Contains(out, "(= HOME)") {
		t.Errorf("writeRuntime lists HOME on the config volume, but HOME=/h lies outside /cfg:\n%s", out)
	}
}

func TestWriteRuntime_GoBinIsGopathBinOnlyWhenGopathIsTheToolsGo(t *testing.T) {
	for _, tc := range []struct {
		name, gopath string
		want         bool
	}{{"the tools go", "/t/go", true}, {"elsewhere", "/g", false}} {
		t.Run(tc.name, func(t *testing.T) {
			fakeEnv(t, map[string]string{"PATH": "/t/bin:/t/go/bin:/a", "GOPATH": tc.gopath})
			var b strings.Builder
			writeRuntime(&b, "/cfg", "/t")
			if got := strings.Contains(b.String(), "`/t/go/bin` is `$GOPATH/bin`"); got != tc.want {
				t.Errorf("GOPATH=%s: claims /t/go/bin is $GOPATH/bin = %v, want %v:\n%s", tc.gopath, got, tc.want, b.String())
			}
		})
	}
}

// A server started without the image's entrypoint can run with the tools bin off PATH, so the doc
// must not promise bare names.
func TestWriteRuntime_ToolsBinOffPathSendsUsersToTheAbsolutePath(t *testing.T) {
	fakeEnv(t, map[string]string{"HOME": "/h", "PATH": "/usr/bin:/bin"})
	fakeUID(t, 1000)
	var b strings.Builder
	writeRuntime(&b, "/cfg", "/srv/t")
	out := b.String()
	for _, want := range []string{
		"the tools engine's single bin dir `/srv/t/bin` is not on it",
		"runs an installed tool by its absolute path (`/srv/t/bin/<tool>`). Pin `HOME=/h`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeRuntime output missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"resolves by bare name", "is the tools engine's single bin dir", "as the image exports it", "/etc/profile"} {
		if strings.Contains(out, bad) {
			t.Errorf("writeRuntime says %q with the tools bin off PATH:\n%s", bad, out)
		}
	}
}

// Only root may write /etc/profile.d, so under another uid a login shell's PATH comes from
// /etc/profile alone and the doc must not promise bare names there.
func TestWriteRuntime_NonRootLoginShellsGetTheAbsolutePath(t *testing.T) {
	fakeEnv(t, map[string]string{"HOME": "/h", "PATH": "/t/bin:/usr/bin:/bin", "MAROTTE_PATH_DROPIN": "1"})
	fakeUID(t, 1000)
	var b strings.Builder
	writeRuntime(&b, "/cfg", "/t")
	out := b.String()
	for _, want := range []string{
		"`/t/bin` is the tools engine's single bin dir",
		"takes its PATH from `/etc/profile`, which leaves out `/t/bin`: marotte runs as uid 1000, not root",
		"runs an installed tool by its absolute path (`/t/bin/<tool>`). Pin `HOME=/h`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeRuntime output missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"resolves by bare name", "10-marotte-path.sh"} {
		if strings.Contains(out, bad) {
			t.Errorf("writeRuntime says %q for a non-root server:\n%s", bad, out)
		}
	}
}

// A root server whose entrypoint did not report a written drop-in (the write failed, or the binary
// started without the entrypoint) must not promise bare names in a login shell.
func TestWriteRuntime_RootWithoutAWrittenDropInGetsTheAbsolutePath(t *testing.T) {
	for name, dropin := range map[string]*string{"unset": nil, "empty": new(""), "not 1": new("yes")} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"HOME": "/h", "PATH": "/t/bin:/usr/bin:/bin"}
			if dropin != nil {
				env["MAROTTE_PATH_DROPIN"] = *dropin
			}
			fakeEnv(t, env)
			fakeUID(t, 0)
			var b strings.Builder
			writeRuntime(&b, "/cfg", "/t")
			out := b.String()
			want := "may not search `/t/bin`: the entrypoint could not give `/etc/profile.d/10-marotte-path.sh` this " +
				"start's PATH (the boot log's WARNING says why, or the server started without the entrypoint). There a " +
				"user runs an installed tool by its absolute path (`/t/bin/<tool>`). Pin `HOME=/h`"
			if !strings.Contains(out, want) {
				t.Errorf("writeRuntime output missing %q:\n%s", want, out)
			}
			if strings.Contains(out, "resolves by bare name") {
				t.Errorf("writeRuntime promises bare names with no written drop-in:\n%s", out)
			}
		})
	}
}

// With no usable tools dir the tools engine is off, so the doc names no tools folder.
func TestWriteRuntime_NoToolsDirNamesNoToolsFolder(t *testing.T) {
	fakeEnv(t, map[string]string{"HOME": "/h", "PATH": "/usr/bin:/bin", "GOPATH": "/go"})
	var b strings.Builder
	writeRuntime(&b, "/cfg", "")
	out := b.String()
	for _, want := range []string{
		"- `PATH=/usr/bin:/bin`: `$HOME/.local/bin` is the per-user bin dir",
		"- The tools engine is off, so it installs nothing. Pin `HOME=/h`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeRuntime output missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"/bin/<tool>", "single bin dir", "tools engine's tree", "`/bin", "`/go/bin"} {
		if strings.Contains(out, bad) {
			t.Errorf("writeRuntime says %q with no tools dir:\n%s", bad, out)
		}
	}
}

func TestWriteRuntime_HomeInsideTheConfigDirIsListedOnTheVolume(t *testing.T) {
	fakeEnv(t, map[string]string{"HOME": "/cfg/home"})
	var b strings.Builder
	writeRuntime(&b, "/cfg", "/cfg/tools")
	out := b.String()
	if !strings.Contains(out, "`home/` (= HOME)") {
		t.Errorf("writeRuntime does not list HOME=/cfg/home on the /cfg volume:\n%s", out)
	}
	if !strings.Contains(out, "Pin `HOME=/cfg/home` on any command") {
		t.Errorf("writeRuntime does not pin the live HOME:\n%s", out)
	}
}

func TestWriteRuntime_UnsetHomeIsNeitherListedNorPinned(t *testing.T) {
	fakeEnv(t, map[string]string{})
	var b strings.Builder
	writeRuntime(&b, "/cfg", "/cfg/tools")
	out := b.String()
	if strings.Contains(out, "HOME") {
		t.Errorf("writeRuntime names HOME, which was unset:\n%s", out)
	}
	if !strings.Contains(out, "path (`/cfg/tools/bin/<tool>`)\n") {
		t.Errorf("writeRuntime does not end the drop-in hint at the absolute path:\n%s", out)
	}
}

// A value carrying a backtick would close the code span it is quoted in, so
// the variable is dropped rather than rendered.
func TestWriteRuntime_NamesTheRuntimeDirAndItsRule(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		fakeEnv(t, map[string]string{"HOME": "/h", "KIRO_RUNTIME_DIR": "/h/.cache/kiro-cli-runtime"})
		var b strings.Builder
		writeRuntime(&b, "/cfg", "/cfg/tools")
		out := b.String()
		for _, want := range []string{"`KIRO_RUNTIME_DIR=/h/.cache/kiro-cli-runtime`", "with `KIRO_RUNTIME_DIR` unset"} {
			if !strings.Contains(out, want) {
				t.Errorf("writeRuntime output missing %q:\n%s", want, out)
			}
		}
	})
	t.Run("unset", func(t *testing.T) {
		fakeEnv(t, map[string]string{"HOME": "/h"})
		var b strings.Builder
		writeRuntime(&b, "/cfg", "/cfg/tools")
		if out := b.String(); strings.Contains(out, "KIRO_RUNTIME_DIR") {
			t.Errorf("writeRuntime named KIRO_RUNTIME_DIR, which was unset:\n%s", out)
		}
	})
}

func TestWriteRuntime_BacktickInValueDropsTheVariable(t *testing.T) {
	fakeEnv(t, map[string]string{
		"HOME":   "/h",
		"PATH":   "/a:/b",
		"GOPATH": "/g`\n## Capabilities",
		"LANG":   "C.UTF-8",
	})
	var b strings.Builder
	writeRuntime(&b, "/cfg", "/cfg/tools")
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
	writeToolsEngine(&b, "/cfg", "/t", "/w")
	out := b.String()
	for _, want := range []string{
		"localhost:9847/api/tools/search?q=<name>",
		"POST localhost:9847/api/tools",
		`"apt":true`,
		"&unavailable=1",
		"Settings → Tools",
		"`/cfg/tools.json`",
		"`/t/opt/<name>/<version>/`",
		"`/t/bin/`",
		"`/cfg/tools.json` is the manifest.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeToolsEngine output missing %q:\n%s", want, out)
		}
	}
	// The Tools tab's controls are UI guidance, which the chat doc carries.
	for _, ui := range []string{`"Refresh catalog"`, `"Update all"`, "UI path for users"} {
		if strings.Contains(out, ui) {
			t.Errorf("writeToolsEngine carries the UI text %q:\n%s", ui, out)
		}
	}
}

func TestWriteUIGuide_DescribesTheToolsTab(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b, "/w", "/cfg")
	line := lineWithPrefix(b.String(), "- Settings → Tools: ")
	for _, want := range []string{`"Tools & runtimes" (a switch per row`, `"Add tool"`, `"Update all"`, `"Refresh catalog"`, `"MCP integrations"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the Settings → Tools bullet lacks %q: %q", want, line)
		}
	}
	if strings.Contains(line, "environment steering doc") {
		t.Errorf("the Settings → Tools bullet still points at environment.md: %q", line)
	}
	// The button text is "Refresh catalog"; the longer string is its aria-label.
	if strings.Contains(line, `"Refresh the tool catalog"`) {
		t.Errorf("the bullet quotes an aria-label as the on-screen label: %q", line)
	}
}

func TestWriteToolsEngine_NoToolsDirSaysTheEngineIsOff(t *testing.T) {
	var b strings.Builder
	writeToolsEngine(&b, "/cfg", "", "/w")
	out := b.String()
	if !strings.Contains(out, "The tools engine is off because the server cannot use the folder configured for it") ||
		!strings.Contains(out, "`MAROTTE_TOOLS_DIR`") {
		t.Errorf("writeToolsEngine does not say the engine is off and which setting moves it:\n%s", out)
	}
	for _, bad := range []string{"/api/tools", "/opt/", "tools.json"} {
		if strings.Contains(out, bad) {
			t.Errorf("writeToolsEngine offers %q with the engine off:\n%s", bad, out)
		}
	}
}

func TestWriteToolsEngine_SaysABadManifestLeavesToolsOffRatherThanStoppingMarotte(t *testing.T) {
	var b strings.Builder
	writeToolsEngine(&b, "/cfg", "/cfg/tools", "/w")
	out := b.String()
	if !strings.Contains(out, "leaves the tools engine off, with the reason in Settings → Tools") {
		t.Errorf("writeToolsEngine does not say a refused tools.json leaves the tools engine off:\n%s", out)
	}
	if strings.Contains(out, "stops marotte from starting") {
		t.Errorf("writeToolsEngine still says an invalid manifest stops marotte:\n%s", out)
	}
}

func TestWriteToolsEngine_ConfigOutsideTheWorkspaceIsNotReachableByFileTools(t *testing.T) {
	var b strings.Builder
	writeToolsEngine(&b, "/cfg", "/cfg/tools", "/w")
	out := b.String()
	if !strings.Contains(out, "Your file tools cannot write it, because `/cfg` is outside the workspace") {
		t.Errorf("writeToolsEngine(/cfg, /w) does not say file tools cannot reach tools.json:\n%s", out)
	}
	if strings.Contains(out, "gets the same check") {
		t.Errorf("writeToolsEngine(/cfg, /w) promises a check file tools never reach:\n%s", out)
	}
}

func TestWriteToolsEngine_ConfigInsideTheWorkspaceGetsTheEditorCheck(t *testing.T) {
	var b strings.Builder
	writeToolsEngine(&b, "/w/.cfg", "/w/.cfg/tools", "/w")
	out := b.String()
	if !strings.Contains(out, "A write from your own file tools gets the same check and install") {
		t.Errorf("writeToolsEngine(/w/.cfg, /w) does not say file-tool writes are checked:\n%s", out)
	}
	if strings.Contains(out, "cannot write it") {
		t.Errorf("writeToolsEngine(/w/.cfg, /w) says file tools cannot reach a manifest inside the workspace:\n%s", out)
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
		`"Refresh accounts and repositories"`,
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
	writeRuntime(&b, "/cfg", "/cfg/tools")
	if cli := forgeCLIName.FindString(b.String()); cli != "" {
		t.Errorf("writeRuntime names the forge CLI %q:\n%s", cli, b.String())
	}
}

// The notifications path is the one a user asks about most; the labels must be
// the exact on-screen text.
func TestWriteUIGuide_NotificationsPath(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b, "/w", "/cfg")
	out := b.String()
	for _, want := range []string{
		"Settings → General",
		`"Push notifications"`,
		`"Agent finished"`,
		`"Pull request checks"`,
		`"Workflow runs"`,
		"has no switch of its own",
		// The one off-by-default kind, named with its reason.
		`"Pull request checks" starts OFF`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeUIGuide output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteUIGuide_NamesTheMemorySetting(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b, "/w", "/cfg")
	out := b.String()
	if !strings.Contains(out, `"Knowledge base search", "Model fallback", "Memory", "Spec planning", "Steering reminders", "Work validation", "AWS CloudFormation safety check", "Workflows", "Output style", "Shell command timeout")`) {
		t.Errorf("writeUIGuide output does not list the Memory setting under Agent capabilities:\n%s", out)
	}
	if strings.Contains(out, "Remember things between conversations") {
		t.Errorf("writeUIGuide output still names the removed memory setting:\n%s", out)
	}
}

func TestWriteUIGuide_NamesEveryTab(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b, "/w", "/cfg")
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
		`"Edit", which opens the page in the editor`,
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

func TestWriteUIGuide_RunTabNamesTheStepEndButtons(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b, "/w", "/cfg")
	out := b.String()
	if !strings.Contains(out, `"Mark step done" / "Mark step failed" on a paused step`) {
		t.Errorf("writeUIGuide run-tab paragraph does not name the step end buttons:\n%s", out)
	}
}

func TestWriteAttachments_UsesTheUploadDirConstant(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/u", "/w", "/cfg")
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
	writeAttachments(&b, "/u", "/w", "/cfg")
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
	writeAttachments(&b, "/u", "/w", "/cfg")
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
		"Every inlined image is fitted first: one over a 2000px long edge is scaled down to it",
		"use Rewind to remove the earlier turn that carried it",
		"Reopening the chat does not clear an image a tool returned",
		"multi-line TEXT over 50 lines or 10,000 characters",
		"a `resource` block whose `uri` is the file path",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
	// validationGuidance (internal/command/prompt.go) never carries this sentence.
	for _, retired := range []string{"model without vision", "embedded the same way"} {
		if strings.Contains(out, retired) {
			t.Errorf("writeAttachments carries the retired phrase %q:\n%s", retired, out)
		}
	}
	// Every inlined image is fitted and a tool-returned image survives a reload.
	for _, wrong := range []string{"only a PASTED image is downscaled", "reopen the chat to"} {
		if strings.Contains(out, wrong) {
			t.Errorf("writeAttachments carries the false claim %q:\n%s", wrong, out)
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
	if !strings.Contains(out, "`/cfg/chats/<id>/entries.jsonl`") {
		t.Errorf("writeCapabilities output missing the chat history path:\n%s", out)
	}
	// Chat-only bullets live in writeChatCapabilities; a copy here would reach steps and the TUI.
	for _, chatOnly := range []string{"Rewind", "Ctrl+F", "no resume or retry tool"} {
		if strings.Contains(out, chatOnly) {
			t.Errorf("writeCapabilities carries the chat-only %q:\n%s", chatOnly, out)
		}
	}
}

func TestWriteChatCapabilities(t *testing.T) {
	var b strings.Builder
	writeChatCapabilities(&b)
	out := b.String()
	for _, want := range []string{
		"## Chat capabilities",
		"Undo is per TURN, not per file",
		"no resume or retry tool",
		"\"Search conversations…\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeChatCapabilities output missing %q:\n%s", want, out)
		}
	}
	// marotte captures nothing per turn.
	if strings.Contains(out, "checkpointed server-side") {
		t.Errorf("writeChatCapabilities repeated the retired per-turn checkpoint claim:\n%s", out)
	}
}

func TestWriteAttachments_TeachesMermaidDiagrams(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/u", "/w", "/cfg")
	out := b.String()
	for _, want := range []string{
		"a fenced block whose language is `mermaid`",
		"once the fence closes it renders as a diagram in the chat",
		"differs slightly from mermaid.js",
		"no HTML, no `%%{init}%%` theme or config directive (marotte themes it), no `click` callback (ignored)",
		"Prefer top-down (`flowchart TD`) to left-right so it fits a phone",
		"split a large one",
		"an `accTitle:` line, which becomes its accessible label and the full-screen view's title",
		"shows its source under a notice",
		"toggle to the source, copy it, download the SVG, or expand it full screen to zoom",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteAttachments_TeachesTheHTMLPreview(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/workspace/.uploads", "/workspace", "/config")
	out := b.String()
	for _, want := range []string{
		"`[Demo](/workspace/demo/index.html)`",
		"its own folder under `/workspace`",
		"is refused",
		"starting with `.`",
		"--base=./`",
		`<meta name="marotte-preview" content="phone">`,
		"you cannot open the tab yourself",
		"`#L<line>`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
}

func TestToolsSection_SaysAShellWriteAppliesWithinSeconds(t *testing.T) {
	var b strings.Builder
	writeToolsEngine(&b, "/cfg", "/t", "/w")
	// A config dir inside the workspace is refused at boot, so no text may describe one.
	writeToolsEngine(&b, "/w/cfg", "/t", "/w")
	out := b.String()
	for _, want := range []string{
		"A shell write is checked and applied within seconds",
		"until a valid save, or a valid shell write, turns it back on",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeToolsEngine output missing %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"neither checked nor applied", "A write from your own file tools"} {
		if strings.Contains(out, gone) {
			t.Errorf("writeToolsEngine output still says %q:\n%s", gone, out)
		}
	}
}
