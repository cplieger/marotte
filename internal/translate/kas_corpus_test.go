package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The corpus under testdata/kas is KAS's live answer to marotte's own bridge, written by
// internal/bridge's TestCaptureKASCorpus. Re-capture it there; never edit a line by hand.
var kasCorpus = []string{"steer", "compact", "delegate", "elicit"}

type kasCaptureHeader struct {
	Recorded string `json:"recorded"`
	KiroCLI  string `json:"kiroCli"`
	KAS      string `json:"kas"`
	Scenario string `json:"scenario"`
}

// kasUpdate is one session/update's (sessionUpdate, update) pair, what EntryProjection.Ingest takes.
type kasUpdate struct {
	Kind   marotte.ACPUpdateKind
	Update json.RawMessage
}

type kasCapture struct {
	Header kasCaptureHeader
	// Live and Replay are the inbound session/update frames either side of the outbound session/load.
	Live, Replay []kasUpdate
	raw          []byte
}

func loadKASCapture(t *testing.T, name string) kasCapture {
	t.Helper()
	path := filepath.Join("testdata", "kas", name+".jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	c := kasCapture{raw: raw}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 4<<20)
	if !sc.Scan() {
		t.Fatalf("%s is empty", path)
	}
	var head struct {
		Meta kasCaptureHeader `json:"_meta"`
	}
	if err := json.Unmarshal(sc.Bytes(), &head); err != nil {
		t.Fatalf("%s header: %v", path, err)
	}
	c.Header = head.Meta
	replay := false
	for sc.Scan() {
		var line struct {
			Dir   string `json:"dir"`
			Frame struct {
				Method string `json:"method"`
				Params struct {
					Update json.RawMessage `json:"update"`
				} `json:"params"`
			} `json:"frame"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if line.Dir == "out" && line.Frame.Method == marotte.MethodSessionLoad {
			replay = true
			continue
		}
		if line.Dir != "in" || line.Frame.Method != marotte.MethodSessionUpdate {
			continue
		}
		var kind struct {
			SessionUpdate marotte.ACPUpdateKind `json:"sessionUpdate"`
		}
		if err := json.Unmarshal(line.Frame.Params.Update, &kind); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		u := kasUpdate{Kind: kind.SessionUpdate, Update: line.Frame.Params.Update}
		if replay {
			c.Replay = append(c.Replay, u)
		} else {
			c.Live = append(c.Live, u)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c
}

func projectUpdates(updates []kasUpdate) []ProjectedTurn {
	p := NewEntryProjection(seqIDs(), "")
	for _, u := range updates {
		p.Ingest(u.Kind, u.Update)
	}
	return p.Turns()
}

// kasMeta decodes an update's _meta.kiro.
func kasMeta(t *testing.T, u kasUpdate) map[string]any {
	t.Helper()
	var f struct {
		Meta struct {
			Kiro map[string]any `json:"kiro"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(u.Update, &f); err != nil {
		t.Fatalf("decode %s update: %v", u.Kind, err)
	}
	return f.Meta.Kiro
}

// findUpdate is the index of the first update at or after from that match accepts; the test fails
// when there is none.
func findUpdate(t *testing.T, updates []kasUpdate, from int, what string, match func(kasUpdate, map[string]any) bool) int {
	t.Helper()
	for i := from; i < len(updates); i++ {
		if match(updates[i], kasMeta(t, updates[i])) {
			return i
		}
	}
	t.Fatalf("the capture holds no %s", what)
	return -1
}

func chunkText(t *testing.T, u kasUpdate) string {
	t.Helper()
	var f struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(u.Update, &f); err != nil {
		t.Fatalf("decode %s update: %v", u.Kind, err)
	}
	return f.Content.Text
}

// withChunkText is u with its content text replaced, every other byte of the frame kept.
func withChunkText(t *testing.T, u kasUpdate, text string) kasUpdate {
	t.Helper()
	var f map[string]any
	if err := json.Unmarshal(u.Update, &f); err != nil {
		t.Fatalf("decode %s update: %v", u.Kind, err)
	}
	content, _ := f["content"].(map[string]any)
	content["text"] = text
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return kasUpdate{Kind: u.Kind, Update: raw}
}

func infoUpdateOf(kind string) func(kasUpdate, map[string]any) bool {
	return func(u kasUpdate, k map[string]any) bool {
		return u.Kind == marotte.ACPUpdateSessionInfo && k["kind"] == kind
	}
}

var (
	capturedEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+\.[A-Za-z][A-Za-z.]+`)
	// The bridge harness refuses these at capture time; this keeps a hand edit from slipping one in.
	capturedForbidden = []string{"/config/", "/workspace/", "/root/", "/home/", "_kiro/auth/", "accessToken"}
)

// TestKASCorpus_Provenance pins that every capture says what recorded it and leaks nothing of the
// host it was recorded on: marotte is public.
func TestKASCorpus_Provenance(t *testing.T) {
	for _, name := range kasCorpus {
		t.Run(name, func(t *testing.T) {
			c := loadKASCapture(t, name)
			if c.Header.Recorded != "live" || c.Header.KiroCLI == "" || c.Header.KAS == "" || c.Header.Scenario == "" {
				t.Errorf("header = %+v, want recorded live with kiroCli, kas and scenario set", c.Header)
			}
			if len(c.Live) == 0 {
				t.Errorf("the capture holds no live session/update")
			}
			for _, bad := range capturedForbidden {
				if bytes.Contains(c.raw, []byte(bad)) {
					t.Errorf("the capture holds %q", bad)
				}
			}
			if m := capturedEmail.Find(c.raw); m != nil {
				t.Errorf("the capture holds an email address %q", m)
			}
		})
	}
}

// TestKASCorpus_EveryKiroMemberIsReadOrDeclined pins that the census stays quiet on the live
// wire: a member KAS really sends is read or declined, so its once-per-process budget is left for
// what KAS adds next.
func TestKASCorpus_EveryKiroMemberIsReadOrDeclined(t *testing.T) {
	for _, name := range kasCorpus {
		t.Run(name, func(t *testing.T) {
			resetCensus(t)
			var logbuf bytes.Buffer
			captureSlog(t, &logbuf)
			c := loadKASCapture(t, name)
			projectUpdates(c.Live)
			projectUpdates(c.Replay)
			// The projection reads session_info_update through its own type; the live door's is sessionInfoUpdate.
			for _, u := range c.Live {
				if u.Kind == marotte.ACPUpdateSessionInfo {
					var info sessionInfoUpdate
					if err := json.Unmarshal(u.Update, &info); err != nil {
						t.Fatalf("decode session_info_update: %v", err)
					}
				}
			}
			if out := logbuf.String(); strings.Contains(out, "UNKNOWN _meta.kiro field") {
				t.Errorf("projecting the capture reported an unread member:\n%s", out)
			}
		})
	}
}
