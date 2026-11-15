package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func getRoute(t *testing.T, rt *Runtime, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	rt.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// seedSetupBot writes the Bot c1 with memory as given, on model-a at high effort, supervised.
func seedSetupBot(t *testing.T, cs *testChatStore, memory bool) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.NameSetByUser, c.CurrentModeID = "CI watcher", true, marotte.BotRoleID
		c.Model, c.Effort, c.SupervisedMode = "model-a", "high", true
		c.Bot = &marotte.Bot{Goal: "Keep CI green.", Steering: "Be brief.", StartedAt: 1_700_000_000_000, Memory: memory, SpecVersion: 3}
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the Bot: %v", err)
	}
}

// writeBotNotes writes c1's memory file under the test HOME's KIRO_HOME, stamped at, and removes it after the test.
func writeBotNotes(t *testing.T, notes string, at time.Time) {
	t.Helper()
	_, file := botMemoryHome(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(file)) })
	if err := os.WriteFile(file, []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, at, at); err != nil {
		t.Fatal(err)
	}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestBotSetupRoute_AnswersTheBotsSetup(t *testing.T) {
	h, cs, _ := newTestHub()
	seedSetupBot(t, cs, true)
	at := time.UnixMilli(1_700_000_456_000)
	writeBotNotes(t, "- seven open\n", at)

	rec := getRoute(t, h, "/api/bots/c1")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/bots/c1 = %d %s, want 200", rec.Code, rec.Body)
	}
	got := decodeBody[marotte.BotSetup](t, rec)
	if got.Bot == nil || got.Bot.Goal != "Keep CI green." || got.Bot.Steering != "Be brief." || got.Bot.SpecVersion != 3 || !got.Bot.Memory {
		t.Errorf("bot = %+v, want the record's bot block", got.Bot)
	}
	if got.Name != "CI watcher" || got.Model != "model-a" || got.Effort != "high" || !got.Supervised {
		t.Errorf("setup = name %q model %q effort %q supervised %v, want the record's", got.Name, got.Model, got.Effort, got.Supervised)
	}
	if got.Memory == nil || got.Memory.Bytes != int64(len("- seven open\n")) || got.Memory.UpdatedAt != at.UnixMilli() {
		t.Errorf("memory = %+v, want %d bytes at %d", got.Memory, len("- seven open\n"), at.UnixMilli())
	}
}

func TestBotSetupRoute_StatesNoMemoryWhileItIsOff(t *testing.T) {
	h, cs, _ := newTestHub()
	seedSetupBot(t, cs, false)

	rec := getRoute(t, h, "/api/bots/c1")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/bots/c1 = %d %s, want 200", rec.Code, rec.Body)
	}
	if got := decodeBody[map[string]json.RawMessage](t, rec); got["memory"] != nil {
		t.Errorf("memory = %s, want absent while memory is off", got["memory"])
	}
}

func TestBotRoutes_RefuseAChatThatIsNoBot(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChain(t, cs, "c-plain", "sess_a")
	for _, path := range []string{"/api/bots/c-plain", "/api/bots/c-plain/memory", "/api/bots/c-gone", "/api/bots/c-gone/memory"} {
		t.Run(path, func(t *testing.T) {
			rec := getRoute(t, h, path)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, rec.Code)
			}
			if got := decodeBody[map[string]string](t, rec); got["error"] == "" {
				t.Errorf("GET %s body = %s, want an error message", path, rec.Body)
			}
		})
	}
}

func TestBotRoutes_RefuseAnIDThatIsNoChatID(t *testing.T) {
	h, _, _ := newTestHub()
	for _, path := range []string{"/api/bots/C%20BAD", "/api/bots/C%20BAD/memory"} {
		if rec := getRoute(t, h, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
}

func TestBotMemoryRoute_AnswersTheNotes(t *testing.T) {
	h, cs, _ := newTestHub()
	seedSetupBot(t, cs, true)
	at := time.UnixMilli(1_700_000_789_000)
	writeBotNotes(t, "- seven open\n", at)

	rec := getRoute(t, h, "/api/bots/c1/memory")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/bots/c1/memory = %d %s, want 200", rec.Code, rec.Body)
	}
	got := decodeBody[marotte.BotMemoryText](t, rec)
	want := marotte.BotMemoryText{Text: "- seven open\n", Bytes: int64(len("- seven open\n")), UpdatedAt: at.UnixMilli()}
	if got != want {
		t.Errorf("memory = %+v, want %+v", got, want)
	}
}

func TestBotMemoryRoute_AnswersEmptyNotesBeforeTheFileExists(t *testing.T) {
	h, cs, _ := newTestHub()
	seedSetupBot(t, cs, true)
	_, file := botMemoryHome(t)
	_ = os.RemoveAll(filepath.Dir(file))

	rec := getRoute(t, h, "/api/bots/c1/memory")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/bots/c1/memory = %d %s, want 200", rec.Code, rec.Body)
	}
	if got := decodeBody[marotte.BotMemoryText](t, rec); got != (marotte.BotMemoryText{}) {
		t.Errorf("memory = %+v, want empty notes", got)
	}
}

func TestBotMemoryRoute_IsNotFoundWhileMemoryIsOff(t *testing.T) {
	h, cs, _ := newTestHub()
	seedSetupBot(t, cs, false)
	writeBotNotes(t, "- left from before\n", time.UnixMilli(1_700_000_000_000))

	rec := getRoute(t, h, "/api/bots/c1/memory")

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/bots/c1/memory = %d, want 404 while memory is off", rec.Code)
	}
	if got := decodeBody[map[string]string](t, rec); got["error"] == "" {
		t.Errorf("body = %s, want an error message", rec.Body)
	}
}

func TestBotMemoryRoute_RefusesALinkedFile(t *testing.T) {
	h, cs, _ := newTestHub()
	seedSetupBot(t, cs, true)
	kiroHome, file := botMemoryHome(t)
	secret := filepath.Join(kiroHome, "bot-routes-secret.json")
	if err := os.WriteFile(secret, []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(file)) })
	if err := os.Symlink(filepath.Join("..", "..", "bot-routes-secret.json"), file); err != nil {
		t.Fatal(err)
	}

	rec := getRoute(t, h, "/api/bots/c1/memory")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /api/bots/c1/memory through a link = %d, want 500", rec.Code)
	}
	if got := decodeBody[map[string]string](t, rec); got["text"] != "" {
		t.Errorf("body = %s, want nothing read through the link", rec.Body)
	}
}
