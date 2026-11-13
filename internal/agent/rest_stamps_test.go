package agent

// GET /api/runs/live (`runs`) and GET /api/config-template (`catalog`) carry the digest stamp with the hub epoch.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/subject"
)

func TestEpoch_IsTheHubsCurrentEpoch(t *testing.T) {
	h, _, _ := newTestHub()
	if got, want := h.Epoch(), h.bus.fanout.Position().Epoch; got != want || got == "" {
		t.Errorf("Epoch() = %q, want the hub's non-empty %q", got, want)
	}
}

func TestLiveRuns_SubjectIsTheLeaseStoresVersionWithTheEpoch(t *testing.T) {
	h, _, _ := newTestHub()
	st := runlease.NewMemory()
	if err := st.Put(t.Context(), &runlease.Lease{WorkflowID: "wf1", ChatID: "c1", Origin: runlease.OriginAgent}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rr := &runRoutes{runs: &Runs{leases: st}, epoch: h.Epoch}

	out := getLiveRuns(t, rr)

	_, version := st.ListStamped()
	want := marotte.SubjectStamp{Kind: string(subject.KindRuns), Version: version, Epoch: h.Epoch()}
	if out.Subject == nil || *out.Subject != want {
		t.Errorf("GET /api/runs/live subject = %+v, want %+v", out.Subject, want)
	}
	if version == "0" || h.Epoch() == "" {
		t.Errorf("version %q / epoch %q: the assertion above is vacuous", version, h.Epoch())
	}
}

// The `runs` subject has one version source: a wake's digest answers the version the inventory read carries,
// so a client holding that read is told nothing changed.
func TestLiveRunsStampIsTheDigestVersion(t *testing.T) {
	h, _, _ := newTestHub()
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{WorkflowID: "wf1", ChatID: "c1", Origin: runlease.OriginAgent}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	out := getLiveRuns(t, h.runRoutes)
	states, err := h.resolveDigest(t.Context(), []sse.Held{held(subject.KindRuns, "")})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}

	digest := stateFor(t, states, subject.KindRuns, "")
	if out.Subject == nil || out.Subject.Version != digest.Version {
		t.Errorf("GET /api/runs/live subject = %+v, digest runs version = %q: want the same version", out.Subject, digest.Version)
	}
	if digest.Version == subject.Unminted {
		t.Errorf("digest runs version = %q after a lease was granted, want a minted version", digest.Version)
	}
}

// Each lease row change reaches every page as one workspace-wide live_run frame stamped `runs` at a strictly greater
// version, and the inventory read serves the version of the last frame it reflects.
func TestLeaseRowChangesBroadcastStampedLiveRunFrames(t *testing.T) {
	h, _, _ := newTestHub()
	store := h.runs.leaseStore()
	before := h.bus.fanout.Position().Head
	ctx := t.Context()
	if err := store.Put(ctx, &runlease.Lease{WorkflowID: "wf1", ChatID: "c1", Origin: runlease.OriginAgent}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.SetDeadline(ctx, "wf1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if err := store.Release(ctx, "wf1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	want := []marotte.LiveRunPayload{
		{WorkflowID: "wf1", ChatID: "c1", Live: true},
		{WorkflowID: "wf1", ChatID: "c1", Live: true, Executing: true},
		{WorkflowID: "wf1", ChatID: "c1"},
	}
	frames := eventsSince(h, before, marotte.EventLiveRun)
	if len(frames) != len(want) {
		t.Fatalf("live_run frames = %d, want one per row change (%d)", len(frames), len(want))
	}
	var last uint64
	for i, e := range frames {
		var msg struct {
			Subject *marotte.SubjectStamp  `json:"subject"`
			ChatID  string                 `json:"chat_id"`
			Payload marotte.LiveRunPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("decode frame %d: %v", i, err)
		}
		if msg.Payload != want[i] || msg.ChatID != "" || e.Event.Topic != "" {
			t.Errorf("frame %d = %+v on chat %q topic %q, want %+v workspace-wide", i, msg.Payload, msg.ChatID, e.Event.Topic, want[i])
		}
		if msg.Subject == nil || msg.Subject.Kind != string(subject.KindRuns) {
			t.Fatalf("frame %d subject = %+v, want a runs stamp", i, msg.Subject)
		}
		v := stampVersion(t, msg.Subject)
		if v <= last {
			t.Errorf("frame %d version %d, want above the previous frame's %d", i, v, last)
		}
		last = v
	}
	out := getLiveRuns(t, h.runRoutes)
	if out.Subject == nil || stampVersion(t, out.Subject) != last {
		t.Errorf("GET /api/runs/live subject = %+v, want the last frame's version %d", out.Subject, last)
	}
}

func TestConfigTemplate_SubjectIsTheCatalogVersionWithTheEpoch(t *testing.T) {
	h, _, _ := newTestHub()
	h.catalog.setModes([]marotte.SessionMode{{ID: "vibe", Name: "Default"}})
	h.catalog.SetModels([]marotte.SessionModel{{ID: "m1", Name: "One"}})

	rec := httptest.NewRecorder()
	h.handleConfigTemplate(rec, httptest.NewRequest(http.MethodGet, "/api/config-template", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got marotte.ConfigTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	current, _ := h.versions.Current(subject.KindCatalog, "")
	want := marotte.SubjectStamp{Kind: string(subject.KindCatalog), Version: current, Epoch: h.Epoch()}
	if got.Subject == nil || *got.Subject != want {
		t.Errorf("GET /api/config-template subject = %+v, want %+v", got.Subject, want)
	}
	if current != "2" {
		t.Errorf("catalog version after SetModes and SetModels = %q, want 2", current)
	}
}

// An unreloaded catalog stamps Unminted, as the resolver answers.
func TestConfigTemplate_UnmintedCatalogStampsZero(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	h.handleConfigTemplate(rec, httptest.NewRequest(http.MethodGet, "/api/config-template", nil))
	var got marotte.ConfigTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Subject == nil || got.Subject.Version != subject.Unminted || got.Subject.Epoch != h.Epoch() {
		t.Errorf("subject = %+v, want version %q with epoch %q", got.Subject, subject.Unminted, h.Epoch())
	}
}
