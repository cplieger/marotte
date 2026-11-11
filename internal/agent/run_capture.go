package agent

// A step's captured output on the run surface: the run read carries each step's capture summary,
// and GET /api/runs/{id}/steps/{path}/capture the whole text, for the shown step's result pane.

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/workflow"
)

// captureSummaryLimit is the most bytes a capture summary holds: past what a collapsed row's two
// clamped lines show at any width.
const captureSummaryLimit = 400

// jsonLenient reads KAS's bytes as encoding/json does: a duplicate name or invalid UTF-8 is
// passed on, not refused.
var jsonLenient = []jsontext.Options{jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)}

// summarizedRead is an inspect reply with each node's `capturedOutput` under `state.root` replaced
// by `captureSummary` (captureSummary) and `hasCapture`. A watch keeps its capture whole: it is
// the structured record its row decodes. Every other value is copied; the moved members go last
// in their node.
func summarizedRead(raw []byte) ([]byte, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(raw), jsonLenient...)
	var out bytes.Buffer
	out.Grow(len(raw) / 2)
	enc := jsontext.NewEncoder(&out, jsonLenient...)
	root := func() error {
		return copyObject(dec, enc, "root", func() error { return summarizeNode(dec, enc) })
	}
	if err := copyObject(dec, enc, "state", root); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// copyObject copies the next value, handing the value of member `into` to inner when it is an
// object's.
func copyObject(dec *jsontext.Decoder, enc *jsontext.Encoder, into string, inner func() error) error {
	if dec.PeekKind() != '{' {
		return copyValue(dec, enc)
	}
	if err := copyToken(dec, enc); err != nil {
		return err
	}
	for dec.PeekKind() != '}' {
		if err := copyMember(dec, enc, into, inner); err != nil {
			return err
		}
	}
	return copyToken(dec, enc)
}

// copyMember copies an object's next member, handing its value to inner when it is `into`.
func copyMember(dec *jsontext.Decoder, enc *jsontext.Encoder, into string, inner func() error) error {
	name, err := dec.ReadToken()
	if err != nil {
		return err
	}
	isInto := name.String() == into
	if wErr := enc.WriteToken(name); wErr != nil {
		return wErr
	}
	if isInto {
		return inner()
	}
	return copyValue(dec, enc)
}

// summarizeNode copies one state node and its children, summarizing a capture.
func summarizeNode(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	if dec.PeekKind() != '{' {
		return copyValue(dec, enc)
	}
	if err := copyToken(dec, enc); err != nil {
		return err
	}
	var n nodeCapture
	for dec.PeekKind() != '}' {
		if err := n.member(dec, enc); err != nil {
			return err
		}
	}
	if n.capture != nil {
		if err := writeCapture(enc, n.kind, n.capture); err != nil {
			return err
		}
	}
	return copyToken(dec, enc)
}

// nodeCapture is what summarizeNode holds of a node until its end: its type and its capture.
type nodeCapture struct {
	kind    string
	capture jsontext.Value
}

// member copies the node's next member, holding back its capture.
func (n *nodeCapture) member(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	name := tok.String()
	if name == "capturedOutput" {
		v, rErr := dec.ReadValue()
		if rErr != nil {
			return rErr
		}
		n.capture = v.Clone()
		return nil
	}
	if wErr := enc.WriteToken(jsontext.String(name)); wErr != nil {
		return wErr
	}
	switch {
	case name == "children" && dec.PeekKind() == '[':
		return copyChildren(dec, enc)
	case name == "type" && dec.PeekKind() == '"':
		v, rErr := dec.ReadValue()
		if rErr != nil {
			return rErr
		}
		if t, uErr := jsontext.AppendUnquote(nil, v); uErr == nil {
			n.kind = string(t)
		}
		return enc.WriteValue(v)
	}
	return copyValue(dec, enc)
}

func copyChildren(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	if err := copyToken(dec, enc); err != nil {
		return err
	}
	for dec.PeekKind() != ']' {
		if err := summarizeNode(dec, enc); err != nil {
			return err
		}
	}
	return copyToken(dec, enc)
}

// writeCapture writes a node's capture members: a watch's or a non-string capture as it came,
// any other as its summary and the flag.
func writeCapture(enc *jsontext.Encoder, kind string, capture jsontext.Value) error {
	text, err := jsontext.AppendUnquote(nil, capture)
	if kind == "watch" || capture.Kind() != '"' || err != nil {
		if err := enc.WriteToken(jsontext.String("capturedOutput")); err != nil {
			return err
		}
		return enc.WriteValue(capture)
	}
	for _, t := range []jsontext.Token{
		jsontext.String("captureSummary"), jsontext.String(captureSummary(string(text))),
		jsontext.String("hasCapture"), jsontext.True,
	} {
		if err := enc.WriteToken(t); err != nil {
			return err
		}
	}
	return nil
}

func copyToken(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	t, err := dec.ReadToken()
	if err != nil {
		return err
	}
	return enc.WriteToken(t)
}

func copyValue(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	v, err := dec.ReadValue()
	if err != nil {
		return err
	}
	return enc.WriteValue(v)
}

// captureSummary is what a collapsed row shows of a capture: its first three lines, at most
// captureSummaryLimit bytes cut on a rune. The row shows two; the third keeps its line clamp's
// ellipsis where the whole capture puts it.
func captureSummary(text string) string {
	end, lines := 0, 0
	for end < len(text) && lines < 3 {
		i := strings.IndexByte(text[end:], '\n')
		if i < 0 {
			end = len(text)
			break
		}
		end += i + 1
		lines++
	}
	if lines == 3 {
		end--
	}
	if end > captureSummaryLimit {
		end = captureSummaryLimit
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
	}
	return text[:end]
}

// handleStepCapture serves GET /api/runs/{id}/steps/{path}/capture: the whole captured output of the
// node at {path}, a percent-encoded workflow.PathKey, absent when it captured nothing. A path the run
// does not name is a 404; an engine that does not answer, a 502.
func (rr *runRoutes) handleStepCapture(w http.ResponseWriter, r *http.Request) {
	id, nodePath := r.PathValue("id"), r.PathValue("path")
	if id == "" || nodePath == "" {
		httpreply.BadRequest(w, "missing workflow id or step path")
		return
	}
	raw, err := rr.runs.rawInspect(r.Context(), id)
	if err != nil {
		writeInspectErr(w, id, err)
		return
	}
	capture, found, err := workflow.CaptureAt(raw, nodePath)
	if err != nil {
		slog.Warn("step capture: run state undecodable", "workflow_id", logsafe.Field(id),
			"node_path", logsafe.Field(nodePath), "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("step capture unavailable"))
		return
	}
	if !found {
		httpreply.NotFound(w, errStepUnknown.Error())
		return
	}
	httpserver.WriteJSON(w, marotte.RunStepCapture{WorkflowID: id, NodePath: nodePath, CapturedOutput: capture})
}
