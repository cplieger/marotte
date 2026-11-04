package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// partialRecord is a store with an unreadable entry ("args" is a string) between two readable ones.
const partialRecord = `{"version":1,"servers":[
{"id":"aaaa","name":"one","transport":"stdio","command":"c1","enabled":true},
{"id":"bbbb","name":"bad","transport":"stdio","command":"cb","args":"x","enabled":true},
{"id":"cccc","name":"two","transport":"stdio","command":"c2","enabled":true}]}`

// badKASEntry is the KAS declaration marotte last rendered for the unreadable "bad" entry.
const badKASEntry = `{"command":"cb","args":["kept"]}`

func seedStoreDir(t *testing.T, record, kasBody string) (dir, kas string) {
	t.Helper()
	dir = t.TempDir()
	kas = filepath.Join(dir, "kas", "mcp.json")
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	if kasBody != "" {
		if err := os.MkdirAll(filepath.Dir(kas), 0o700); err != nil {
			t.Fatal(err)
		}
		writeKASFile(t, kas, kasBody)
	}
	return dir, kas
}

func openSeeded(t *testing.T, dir, kas string) *Store {
	t.Helper()
	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// listBody is GET /api/mcp as the browser reads it.
func listBody(t *testing.T, s *Store) (names []string, unreadable int) {
	t.Helper()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/mcp", http.NoBody))
	var body struct {
		Servers []struct {
			Name string `json:"name"`
		} `json:"servers"`
		Unreadable int `json:"unreadable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /api/mcp body %s: %v", rec.Body.String(), err)
	}
	for _, sv := range body.Servers {
		names = append(names, sv.Name)
	}
	return names, body.Unreadable
}

func kasServerKeys(t *testing.T, kas string) map[string]json.RawMessage {
	t.Helper()
	return objectOrEmpty(readKAS(t, kas)[kasServerKey])
}

func TestLoad_OneUnreadableEntryAmongThreeLoadsTheOtherTwo(t *testing.T) {
	dir, kas := seedStoreDir(t, partialRecord, "")
	logs := captureSlog(t)
	s := openSeeded(t, dir, kas)

	names, unreadable := listBody(t, s)
	if strings.Join(names, ",") != "one,two" {
		t.Errorf("listed servers = %v, want [one two]", names)
	}
	if unreadable != 1 {
		t.Errorf("unreadable = %d, want 1", unreadable)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "index=1") {
		t.Errorf("no WARN naming entry index 1:\n%s", logs.String())
	}
	siblings := corruptSiblings(t, dir)
	if len(siblings) != 1 {
		t.Fatalf("corrupt siblings = %v, want one copy of the original", siblings)
	}
	if got, err := os.ReadFile(filepath.Join(dir, siblings[0])); err != nil || string(got) != partialRecord {
		t.Errorf("copy = %q, %v; want the original bytes", got, err)
	}
	if info, err := os.Stat(filepath.Join(dir, siblings[0])); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("copy mode = %v, %v; want 0600", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mcp.json")); err != nil {
		t.Errorf("the record must stay in place: %v", err)
	}
}

func TestLoad_ANullEntryIsUnreadable(t *testing.T) {
	record := `{"version":1,"servers":[null,{"id":"aaaa","name":"one","transport":"stdio","command":"c1","enabled":true}]}`
	dir, kas := seedStoreDir(t, record, "")
	captureSlog(t)
	s := openSeeded(t, dir, kas)

	names, unreadable := listBody(t, s)
	if strings.Join(names, ",") != "one" || unreadable != 1 {
		t.Errorf("list = %v, unreadable %d; want [one], 1", names, unreadable)
	}
}

func TestHeal_KeepsAnUnreadableEntrysLastKASDeclaration(t *testing.T) {
	dir, kas := seedStoreDir(t, partialRecord,
		`{"mcpServers":{"one":{"command":"c1"},"bad":`+badKASEntry+`,"two":{"command":"c2"}}}`)
	captureSlog(t)
	s := openSeeded(t, dir, kas)

	if _, err := s.HealKASConfig(t.Context(), "poll"); err != nil {
		t.Fatalf("HealKASConfig: %v", err)
	}
	got := kasServerKeys(t, kas)
	if len(got) != 3 || !sameJSONValue(got["bad"], json.RawMessage(badKASEntry)) {
		t.Errorf("kas mcpServers after heal = %s, want one, two and bad's last declaration", readKAS(t, kas)[kasServerKey])
	}
	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || healed {
		t.Errorf("second heal = %v, %v; a held entry is not drift", healed, err)
	}

	if _, err := s.Create(t.Context(), &Server{Name: "three", Transport: TransportStdio, Command: "c3", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := kasServerKeys(t, kas); len(got) != 4 || !sameJSONValue(got["bad"], json.RawMessage(badKASEntry)) {
		t.Errorf("kas mcpServers after a save = %s, want bad still held", readKAS(t, kas)[kasServerKey])
	}
	record, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil || !strings.Contains(string(record), `"args": "x"`) {
		t.Errorf("a save dropped the unreadable entry from the record:\n%s", record)
	}

	reopened := openSeeded(t, dir, kas)
	if names, unreadable := listBody(t, reopened); len(names) != 3 || unreadable != 1 {
		t.Errorf("after a restart: list %v, unreadable %d; want 3 servers and 1 unreadable", names, unreadable)
	}
	if _, err := reopened.HealKASConfig(t.Context(), "poll"); err != nil {
		t.Fatalf("HealKASConfig after restart: %v", err)
	}
	if got := kasServerKeys(t, kas); !sameJSONValue(got["bad"], json.RawMessage(badKASEntry)) {
		t.Errorf("after a restart the heal dropped bad: %s", readKAS(t, kas)[kasServerKey])
	}
}

// An entry whose name does not read holds every key KAS's file had at boot that no readable
// server renders, and nothing a later delete removes.
func TestHeal_ANamelessUnreadableEntryHoldsTheBootOrphans(t *testing.T) {
	record := `{"version":1,"servers":[
{"id":"aaaa","name":"one","transport":"stdio","command":"c1","enabled":true},
{"id":"bbbb","name":5,"transport":"stdio","command":"cb","enabled":true}]}`
	dir, kas := seedStoreDir(t, record, `{"mcpServers":{"one":{"command":"c1"},"orphan":`+badKASEntry+`}}`)
	captureSlog(t)
	s := openSeeded(t, dir, kas)

	if _, err := s.HealKASConfig(t.Context(), "poll"); err != nil {
		t.Fatalf("HealKASConfig: %v", err)
	}
	if got := kasServerKeys(t, kas); !sameJSONValue(got["orphan"], json.RawMessage(badKASEntry)) {
		t.Errorf("kas mcpServers = %s, want the boot orphan held", readKAS(t, kas)[kasServerKey])
	}

	if err := s.delete(t.Context(), "aaaa"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got := kasServerKeys(t, kas)
	if _, ok := got["one"]; ok {
		t.Errorf("a deleted readable server stayed in KAS's file: %s", readKAS(t, kas)[kasServerKey])
	}
	if _, ok := got["orphan"]; !ok {
		t.Errorf("the delete dropped the held orphan: %s", readKAS(t, kas)[kasServerKey])
	}
	writeKASFile(t, kas, `{"mcpServers":{"orphan":`+badKASEntry+`,"later":{"command":"l"}}}`)
	if _, err := s.HealKASConfig(t.Context(), "poll"); err != nil {
		t.Fatalf("HealKASConfig: %v", err)
	}
	if _, ok := kasServerKeys(t, kas)["later"]; ok {
		t.Errorf("a key added after boot was held: %s", readKAS(t, kas)[kasServerKey])
	}
}

func TestLoad_ShapesThatKeepTodaysBehaviour(t *testing.T) {
	for _, tc := range []struct{ name, record string }{
		{name: "every entry unreadable", record: `{"version":1,"servers":[{"id":"bbbb","name":"bad","args":"x"},{"id":"cccc","name":"worse","env":"y"}]}`},
		{name: "not an object", record: `[{"id":"aaaa","name":"one","transport":"stdio","command":"c1","enabled":true}]`},
		{name: "servers not an array", record: `{"version":1,"servers":{"one":{"id":"aaaa"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, kas := seedStoreDir(t, tc.record, `{"mcpServers":{"one":{"command":"c1"}}}`)
			logs := captureSlog(t)
			s := openSeeded(t, dir, kas)

			if names, unreadable := listBody(t, s); len(names) != 0 || unreadable != 0 {
				t.Errorf("list = %v, unreadable %d; want an empty store", names, unreadable)
			}
			if _, err := os.Stat(filepath.Join(dir, "mcp.json")); !os.IsNotExist(err) {
				t.Errorf("the record was not moved aside: %v", err)
			}
			if siblings := corruptSiblings(t, dir); len(siblings) != 1 {
				t.Errorf("corrupt siblings = %v, want one", siblings)
			}
			if !strings.Contains(logs.String(), "moved aside") {
				t.Errorf("no 'moved aside' WARN:\n%s", logs.String())
			}
		})
	}
}
