package mcp

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// waitServers is one stdio, one http and one disabled server, none of which sets
// its own wait, so every rendered waitForReady comes from the global switch.
func waitServers() []*Server {
	return []*Server{
		{Name: "local", Transport: TransportStdio, Command: "npx", Enabled: true},
		{Name: "remote", Transport: TransportHTTP, URL: "https://mcp.example.com", Enabled: true},
		{Name: "off", Transport: TransportStdio, Command: "npx", Enabled: false},
	}
}

func TestRenderKASServers_WaitAllMarksEveryServer(t *testing.T) {
	got := renderKASServers(waitServers(), kasRenderPolicy{waitAll: true})
	for _, name := range []string{"local", "remote", "off"} {
		if entry, ok := got[name]; !ok || !entry.WaitForReady {
			t.Errorf("renderKASServers(waitAll)[%q] = %+v, want waitForReady true", name, entry)
		}
	}
}

func TestRenderKASServers_PerServerPassThroughWhenWaitAllOff(t *testing.T) {
	servers := append(waitServers(), &Server{
		Name: "pasted", Transport: TransportStdio, Command: "npx", Enabled: true,
		WaitForReady: true, TimeoutMS: 120_000,
	})
	got := renderKASServers(servers, kasRenderPolicy{})
	if p := got["pasted"]; !p.WaitForReady || p.Timeout != 120_000 {
		t.Errorf("pasted entry = %+v, want waitForReady true and timeout 120000", p)
	}
	if l := got["local"]; l.WaitForReady || l.Timeout != 0 {
		t.Errorf("default entry = %+v, want neither waitForReady nor timeout", l)
	}
}

// A default record must keep both keys out of the file entirely: an explicit
// false or 0 is a different declaration to KAS than an absent field.
func TestWriteKASConfig_DefaultRecordEmitsNoWaitKeys(t *testing.T) {
	s, kasPath := newIsolatedStore(t)
	if _, err := s.Create(t.Context(), waitServers()[0]); err != nil {
		t.Fatalf("Create: %v", err)
	}
	entry := readKASServers(t, kasPath)["local"]
	for _, key := range []string{"waitForReady", "timeout"} {
		if v, ok := entry[key]; ok {
			t.Errorf("default record rendered %s = %v, want the key absent", key, v)
		}
	}
}

func TestRenderKASConfig_ResolvesWaitForReadyPerWrite(t *testing.T) {
	dir := t.TempDir()
	kasPath := filepath.Join(dir, "kas", "mcp.json")
	var on atomic.Bool
	s, err := New(t.Context(), dir, nil, WithKASConfigPath(kasPath),
		WithWaitForReady(func(context.Context) bool { return on.Load() }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Create(t.Context(), waitServers()[0]); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := readKASServers(t, kasPath)["local"]["waitForReady"]; ok {
		t.Fatal("waitForReady rendered with the setting off")
	}
	on.Store(true)
	if err := s.RenderKASConfig(t.Context()); err != nil {
		t.Fatalf("RenderKASConfig: %v", err)
	}
	if v := readKASServers(t, kasPath)["local"]["waitForReady"]; v != true {
		t.Errorf("after the flip, waitForReady = %v, want true: the resolver must be read per write", v)
	}
}

func TestNew_UnwiredWaitForReadyRendersNoWait(t *testing.T) {
	s, kasPath := newIsolatedStore(t)
	if _, err := s.Create(t.Context(), waitServers()[1]); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v, ok := readKASServers(t, kasPath)["remote"]["waitForReady"]; ok {
		t.Errorf("a store with no resolver rendered waitForReady = %v, want the key absent", v)
	}
}

// The boot reconcile rebuilds mcpServers from the store, so a pasted wait must
// live on the record or a restart erases it.
func TestWriteKASConfig_PerServerWaitForReadySurvivesReRender(t *testing.T) {
	dir := t.TempDir()
	kasPath := filepath.Join(dir, "kas", "mcp.json")
	s, err := New(t.Context(), dir, nil, WithKASConfigPath(kasPath))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Create(t.Context(), &Server{
		Name: "pasted", Transport: TransportStdio, Command: "npx", Enabled: true,
		WaitForReady: true, TimeoutMS: 90_000,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := New(t.Context(), dir, nil, WithKASConfigPath(kasPath)); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	entry := readKASServers(t, kasPath)["pasted"]
	if entry["waitForReady"] != true || entry["timeout"] != float64(90_000) {
		t.Errorf("after the boot re-render, entry = %v, want waitForReady true and timeout 90000", entry)
	}
}
