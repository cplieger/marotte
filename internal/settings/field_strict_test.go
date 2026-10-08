package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/slogx/capture"
)

// TestFieldStrict_SeparatesAbsenceFromUnreadable walks one config.json through present,
// unparseable and absent, on one directory so the cache runs as in a live process. Folding
// the middle leg into absence would override a stored -1 (Keep forever) and purge kept chats.
func TestFieldStrict_SeparatesAbsenceFromUnreadable(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	resetCache(t, dir)
	path := filepath.Join(dir, Filename)

	// Leg 1: -1 is the Keep-forever value a default would destroy.
	writeSettings(t, dir, []byte(`{"chat_retention_days":-1}`))
	days, ok, err := FieldStrict[int](ctx, dir, KeyChatRetentionDays)
	if err != nil {
		t.Fatalf("FieldStrict(%s) on a stored value: err = %v, want nil", KeyChatRetentionDays, err)
	}
	if !ok || days != -1 {
		t.Fatalf("FieldStrict(%s) on a stored value = (%d, %v), want (-1, true)", KeyChatRetentionDays, days, ok)
	}

	// Leg 2: present and unparseable; absence would be a lie.
	writeSettings(t, dir, []byte(`{`))
	days, ok, err = FieldStrict[int](ctx, dir, KeyChatRetentionDays)
	if err == nil {
		t.Errorf("FieldStrict(%s) on an unparseable config.json = (%d, %v, nil), want an error", KeyChatRetentionDays, days, ok)
	}
	if ok || days != 0 {
		t.Errorf("FieldStrict(%s) on an unparseable config.json = (%d, %v), want (0, false)", KeyChatRetentionDays, days, ok)
	}

	// Leg 3: no file. A fresh volume must stay absent, or no install ever purges.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	days, ok, err = FieldStrict[int](ctx, dir, KeyChatRetentionDays)
	if err != nil {
		t.Errorf("FieldStrict(%s) with no config.json: err = %v, want nil (absence is not a failure)", KeyChatRetentionDays, err)
	}
	if ok || days != 0 {
		t.Errorf("FieldStrict(%s) with no config.json = (%d, %v), want (0, false)", KeyChatRetentionDays, days, ok)
	}
}

// TestFieldStrict_AbsentKeyInAReadableFileIsNotAnError pins that a good file predating the
// key answers absence with a nil error.
func TestFieldStrict_AbsentKeyInAReadableFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"theme":"dark"}`))

	days, ok, err := FieldStrict[int](t.Context(), dir, KeyChatRetentionDays)
	if err != nil || ok || days != 0 {
		t.Errorf("FieldStrict(%s) on a file without the key = (%d, %v, %v), want (0, false, nil)",
			KeyChatRetentionDays, days, ok, err)
	}
}

// TestField_KeepsBothWarnLines pins Field's two distinct Warn lines (unparseable document vs
// wrong-typed key), the operator's only signal. Serial: slog's default is process-global.
func TestField_KeepsBothWarnLines(t *testing.T) {
	tests := []struct {
		desc    string
		content string
		wantMsg string
	}{
		{
			desc:    "a document that does not parse",
			content: `{`,
			wantMsg: "settings: read config.json for " + KeyChatRetentionDays,
		},
		{
			desc:    "a key whose value is the wrong type",
			content: `{"chat_retention_days":"forever"}`,
			wantMsg: "settings: parse " + KeyChatRetentionDays,
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			resetCache(t, dir)
			writeSettings(t, dir, []byte(tc.content))

			rec := capture.Default(t)
			if _, ok := Field[int](t.Context(), dir, KeyChatRetentionDays); ok {
				t.Fatalf("Field(%s) on %s returned ok=true", KeyChatRetentionDays, tc.desc)
			}
			if n := rec.CountExact(tc.wantMsg); n != 1 {
				t.Errorf("Field(%s) on %s logged %q %d times, want 1; messages: %v",
					KeyChatRetentionDays, tc.desc, tc.wantMsg, n, rec.Messages())
			}
		})
	}
}

func TestFieldOr_IsNoAnswerOnlyOverAnUnreadableDocument(t *testing.T) {
	tests := []struct {
		desc         string
		content      string
		want         bool
		wantReadable bool
	}{
		{desc: "no config.json", want: true, wantReadable: true},
		{desc: "an absent key", content: `{}`, want: true, wantReadable: true},
		{desc: "a key of the wrong type", content: `{"debug_logs":"yes"}`, want: true, wantReadable: true},
		{desc: "a stored value", content: `{"debug_logs":false}`, wantReadable: true},
		{desc: "a document that does not parse", content: `{`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			resetCache(t, dir)
			if tc.content != "" {
				writeSettings(t, dir, []byte(tc.content))
			}

			got, readable := FieldOr(t.Context(), dir, KeyDebugLogs, true)

			if got != tc.want || readable != tc.wantReadable {
				t.Errorf("FieldOr(%s, def true) over %s = (%v, readable %v), want (%v, readable %v)",
					KeyDebugLogs, tc.desc, got, readable, tc.want, tc.wantReadable)
			}
		})
	}
}
