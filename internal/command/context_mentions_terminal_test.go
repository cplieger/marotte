package command

import (
	"strings"
	"testing"
	"unicode/utf8"
)

type fakeShell struct {
	out    ShellText
	asked  int
	notRun bool
}

func (f *fakeShell) ReadShell(maxLines int) (ShellText, bool) {
	f.asked = maxLines
	return f.out, !f.notRun
}

func terminalMention(t *testing.T, token string, sh shellReader) string {
	t.Helper()
	ws := testWorkspace(t)
	ws.Terminal = sh
	return mentionText(t, BuildPromptBlocks(t.Context(), "see "+token, nil, 0, ws, nil), token)
}

func TestBuildPromptBlocks_MentionTerminalSendsTheShellOutputUnderTheIDEHeader(t *testing.T) {
	sh := &fakeShell{out: ShellText{Text: "$ make\nok"}}
	got := terminalMention(t, "#[[terminal:]]", sh)
	if want := "Current terminal contents:\n$ make\nok"; got != want {
		t.Errorf("terminal mention = %q, want %q", got, want)
	}
	if sh.asked != 200 {
		t.Errorf("ReadShell(%d), want the default 200 lines", sh.asked)
	}
}

func TestBuildPromptBlocks_MentionTerminalLineCount(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "explicit", query: "50", want: 50},
		{name: "spaced", query: " 7 ", want: 7},
		{name: "over_the_ceiling", query: "999999", want: 5000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sh := &fakeShell{out: ShellText{Text: "x"}}
			terminalMention(t, "#[[terminal:"+tc.query+"]]", sh)
			if sh.asked != tc.want {
				t.Errorf("#[[terminal:%s]] asked ReadShell(%d), want %d", tc.query, sh.asked, tc.want)
			}
		})
	}
}

func TestBuildPromptBlocks_MentionTerminalSendsAReason(t *testing.T) {
	tests := []struct {
		sh    shellReader
		name  string
		token string
		want  string
	}{
		{name: "bad_count", token: "#[[terminal:ten]]", sh: &fakeShell{}, want: errTerminalLines.Error()},
		{name: "zero_count", token: "#[[terminal:0]]", sh: &fakeShell{}, want: errTerminalLines.Error()},
		{name: "no_reader", token: "#[[terminal:]]", sh: nil, want: errShellUnavailable.Error()},
		{name: "not_started", token: "#[[terminal:]]", sh: &fakeShell{notRun: true}, want: errShellNotStarted.Error()},
		{name: "empty", token: "#[[terminal:]]", sh: &fakeShell{}, want: errTerminalEmpty.Error()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := terminalMention(t, tc.token, tc.sh)
			want := "[Unresolved context reference: " + tc.token + " (" + tc.want + ")]"
			if got != want {
				t.Errorf("terminal mention = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildPromptBlocks_MentionTerminalNotesAFullScreenProgramAndAnExit(t *testing.T) {
	sh := &fakeShell{out: ShellText{Text: "$ vim notes", AltScreen: true, Exited: true}}
	got := terminalMention(t, "#[[terminal:]]", sh)
	want := terminalHeader + terminalAltNote + "$ vim notes" + terminalExitNote
	if got != want {
		t.Errorf("terminal mention = %q, want %q", got, want)
	}
}

func TestBuildPromptBlocks_MentionTerminalOverTheCapKeepsTheNewestLines(t *testing.T) {
	var b strings.Builder
	for i := range 6000 {
		b.WriteString(strings.Repeat("é", 9))
		b.WriteString(" line ")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	b.WriteString("NEWEST")
	sh := &fakeShell{out: ShellText{Text: b.String(), Exited: true}}

	got := terminalMention(t, "#[[terminal:]]", sh)

	if n := utf8.RuneCountInString(got); n > maxMentionChars {
		t.Errorf("terminal mention is %d characters, want at most %d", n, maxMentionChars)
	}
	if want := terminalHeader + terminalOmitted + strings.Repeat("é", 9); !strings.HasPrefix(got, want) {
		t.Errorf("terminal mention starts %q, want the header, the omission note, then a whole line", got[:min(len(got), 80)])
	}
	if !strings.HasSuffix(got, "NEWEST"+terminalExitNote) {
		t.Errorf("terminal mention ends %q, want the newest line and the exit note", got[max(0, len(got)-40):])
	}
	if strings.Contains(got, "[truncated at") {
		t.Error("terminal mention carries the head-keeping truncation marker; the cut must keep the newest output")
	}
}

func TestKeepNewest_CutsTheOldestEnd(t *testing.T) {
	tests := []struct {
		name, in, want string
		n              int
	}{
		{name: "fits", in: "a\nb", n: 10, want: "a\nb"},
		{name: "line_boundary", in: "aaaa\nbb\ncc", n: 6, want: "bb\ncc"},
		{name: "long_last_line", in: "a\nbbbbbbbb", n: 3, want: "bbb"},
		{name: "multibyte", in: "x\néé", n: 2, want: "éé"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := keepNewest(tc.in, tc.n); got != tc.want {
				t.Errorf("keepNewest(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}
