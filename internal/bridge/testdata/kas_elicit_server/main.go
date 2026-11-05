// Command kas_elicit_server is a stdio MCP server with one tool, ask_value, that sends the client an
// elicitation/create form and returns the answer as its result. TestCaptureKASCorpus runs it so the
// elicit capture holds KAS relaying a real MCP elicitation and taking marotte's answer back.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

const elicitID = `"kas-capture-elicit"`

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 16<<20)
	out := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := out.Encode(v); err != nil {
			os.Exit(1)
		}
	}
	reply := func(id json.RawMessage, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	for in.Scan() {
		var m message
		if json.Unmarshal(in.Bytes(), &m) != nil || m.Method == "" || m.ID == nil {
			continue
		}
		switch m.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(m.Params, &p)
			reply(m.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "kas-capture-elicit", "version": "1.0.0"},
			})
		case "tools/list":
			reply(m.ID, map[string]any{"tools": []any{map[string]any{
				"name":        "ask_value",
				"description": "Asks the user for a value through a form and returns what they entered.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			}}})
		case "tools/call":
			reply(m.ID, askValue(in, send))
		case "ping":
			reply(m.ID, map[string]any{})
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"error": map[string]any{"code": -32601, "message": "method not found: " + m.Method}})
		}
	}
}

// askValue sends the elicitation and reads until its answer arrives, answering a ping meanwhile.
func askValue(in *bufio.Scanner, send func(any)) map[string]any {
	send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(elicitID), "method": "elicitation/create",
		"params": map[string]any{
			"message": "Enter the value the tool should record.",
			"requestedSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "string", "title": "Value"},
				},
				"required": []string{"value"},
			},
		}})
	for in.Scan() {
		var m message
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "ping" && m.ID != nil {
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
			continue
		}
		if string(m.ID) != elicitID || m.Method != "" {
			continue
		}
		if m.Error != nil {
			return toolText(fmt.Sprintf("elicitation failed: %s", m.Error), true)
		}
		var r struct {
			Content map[string]any `json:"content"`
			Action  string         `json:"action"`
		}
		_ = json.Unmarshal(m.Result, &r)
		if r.Action != "accept" {
			return toolText("the user answered "+r.Action, false)
		}
		return toolText(fmt.Sprintf("recorded value: %v", r.Content["value"]), false)
	}
	return toolText("the client closed the connection before answering", true)
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}
