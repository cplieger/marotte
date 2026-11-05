package mcp

import (
	"encoding/json"
	"maps"
	"testing"
)

// A write that changes nothing reproduces the file, and a write of marotte's keys leaves a file
// KAS reads with every other member unchanged and marotte's keys holding what was written.
func FuzzKASDoc_patchKeepsOtherMembers(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"mcpServers": {}}`,
		"// top\n{\n  /* a */ \"a\": [1, 2,], // after a\n  \"mcpServers\": {\"x\": {\"command\": \"c\"}, // x\n  },\n}\n",
		"{\"powers\": {\"other\": 1, \"mcpServers\": {\"p\": {}}}, \"mcpServers\": null, \"mcpServers\": {}}",
		`{"a":1 /* c */}`,
		"{\"mcpServers\": {\"gh\": {\"args\": [\"-y\"]}}} // end",
		"/* n */ null",
	} {
		f.Add(seed)
	}
	wantServers := json.RawMessage(`{"gh":{"command":"bash","args":["-y"]},"new":{"url":"https://e.example"}}`)
	wantPowers := json.RawMessage(`{"mcpServers":{"power-a-b":{"command":"c"}}}`)
	f.Fuzz(func(t *testing.T, data string) {
		unchanged, fresh, err := parseKASDoc([]byte(data))
		if err != nil {
			return
		}
		if !fresh {
			out, err := unchanged.render()
			if err != nil {
				t.Fatalf("render unchanged: %v", err)
			}
			body, _ := decodeUserJSON([]byte(data))
			if got := string(out); got != string(body) && got != string(body)+"\n" {
				t.Fatalf("an unchanged write rewrote the file:\n%q\nto\n%q", body, got)
			}
		}

		doc, _, err := parseKASDoc([]byte(data))
		if err != nil {
			t.Fatalf("second parse: %v", err)
		}
		before := maps.Clone(doc.members)
		doc.members[kasServerKey] = wantServers
		doc.members[kasPowersKey] = wantPowers
		out, err := doc.render()
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		again, _, err := parseKASDoc(out)
		if err != nil {
			t.Fatalf("the write is not readable: %v\n%s", err, out)
		}
		for k, v := range before {
			if k != kasServerKey && k != kasPowersKey && !sameJSONValue(again.members[k], v) {
				t.Errorf("member %q changed from %s to %s", k, v, again.members[k])
			}
		}
		if !sameJSONValue(again.members[kasServerKey], wantServers) || !sameJSONValue(again.members[kasPowersKey], wantPowers) {
			t.Errorf("marotte's keys = %s, %s; want %s, %s\n%s",
				again.members[kasServerKey], again.members[kasPowersKey], wantServers, wantPowers, out)
		}
	})
}
