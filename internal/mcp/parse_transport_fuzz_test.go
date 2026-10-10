package mcp

import "testing"

func FuzzParseTransport(f *testing.F) {
	f.Add("stdio")
	f.Add("http")
	f.Add("sse")
	f.Add("")
	f.Add("STDIO")
	f.Add("unknown")

	f.Fuzz(func(t *testing.T, s string) {
		tr, err := parseTransport(s)
		if err != nil {
			return
		}
		if !tr.valid() {
			t.Fatalf("ParseTransport(%q) returned invalid transport %q", s, tr)
		}
		// sse is a first-class transport, not folded into http:
		// KAS accepts a distinct {type:"sse"} mcpServers entry on the v3
		// wire, so parseTransport preserves it as TransportSSE.
		if s == "sse" && tr != TransportSSE {
			t.Fatalf("sse must map to TransportSSE, got %q", tr)
		}
	})
}
