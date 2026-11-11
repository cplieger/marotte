package workflow

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"math"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// EarlierPasses is what a glance keeps of a repeat's passes before its latest: how many, their
// credits, and the window of their work nodes in Unix milliseconds (From 0 when none started).
// Open is a started work node with no end.
type EarlierPasses struct {
	Count   int     `json:"count"`
	Credits float64 `json:"credits,omitempty"`
	From    int64   `json:"from,omitempty"`
	To      int64   `json:"to,omitempty"`
	Open    bool    `json:"open,omitempty"`
}

// GlanceRead is a run read as a run's glance: each repeat under `state.root` keeps only its
// latest pass (its last child) and folds the passes before it into its `earlierPasses`; every
// other value is copied. starts is the run log's attempt starts, which the folded window reads as
// the client's tree does. paths is the PathKey of every node kept.
func GlanceRead(raw []byte, starts map[string]marotte.RunStepStart) (out json.RawMessage, paths map[string]bool, err error) {
	dec := jsontext.NewDecoder(bytes.NewReader(raw), indexLenient...)
	g := glancer{starts: starts, paths: map[string]bool{}}
	g.out.Grow(len(raw) / 4)
	err = g.object(dec, func(name string) (bool, error) {
		if name != "state" || dec.PeekKind() != '{' {
			return false, nil
		}
		return true, g.object(dec, func(member string) (bool, error) {
			if member != "root" || dec.PeekKind() != '{' {
				return false, nil
			}
			n, rErr := readIndexNode(dec)
			if rErr != nil {
				return true, rErr
			}
			g.node(n, nil, "")
			return true, nil
		})
	})
	if err != nil {
		return nil, nil, fmt.Errorf("glance: %w", err)
	}
	return g.out.Bytes(), g.paths, nil
}

type glancer struct {
	starts map[string]marotte.RunStepStart
	paths  map[string]bool
	out    bytes.Buffer
}

// object copies the object at dec, handing each member's name to inner, which writes the value
// itself and answers true, or leaves it to be copied.
func (g *glancer) object(dec *jsontext.Decoder, inner func(name string) (bool, error)) error {
	sep := false
	g.out.WriteByte('{')
	err := eachMember(dec, func(name string) error {
		if sep {
			g.out.WriteByte(',')
		}
		sep = true
		b, _ := jsontext.AppendQuote(g.out.AvailableBuffer(), name)
		g.out.Write(b)
		g.out.WriteByte(':')
		done, err := inner(name)
		if err != nil || done {
			return err
		}
		v, err := dec.ReadValue()
		g.out.Write(v)
		return err
	})
	g.out.WriteByte('}')
	return err
}

func (g *glancer) node(n *indexNode, trail []string, parentType string) {
	path := append(append(make([]string, 0, len(trail)+1), trail...), segmentOf(n.nodeID, n.iteration, parentType))
	g.paths[PathKey(path)] = true
	kids, earlier := g.fold(n, path)
	g.out.Write(n.own[:len(n.own)-1])
	sep := len(n.own) > 2
	if n.listed {
		if sep {
			g.out.WriteByte(',')
		}
		sep = true
		g.out.WriteString(`"children":[`)
		for i, k := range kids {
			if i > 0 {
				g.out.WriteByte(',')
			}
			g.node(k, path, n.kind)
		}
		g.out.WriteByte(']')
	}
	if earlier != nil {
		if sep {
			g.out.WriteByte(',')
		}
		b, _ := json.Marshal(earlier)
		g.out.WriteString(`"earlierPasses":`)
		g.out.Write(b)
	}
	g.out.WriteByte('}')
}

// fold is the children a glance keeps of n at path, and a repeat's fold of the passes it leaves out.
func (g *glancer) fold(n *indexNode, path []string) (kept []*indexNode, earlier *EarlierPasses) {
	if n.kind != nodeTypeRepeat || len(n.children) < 2 {
		return n.children, nil
	}
	last := len(n.children) - 1
	w := passWindow{starts: g.starts}
	for _, k := range n.children[:last] {
		w.add(k, path, n.kind)
	}
	return n.children[last:], w.summary(last)
}

// passWindow folds folded passes' nodes: the client's windowBounds over their work nodes (each
// start its attempt's, as the run store's attemptStart picks it) and runCredits over every node.
type passWindow struct {
	starts  map[string]marotte.RunStepStart
	credits float64
	from    int64
	to      int64
	open    bool
}

// foldedNode is what passWindow reads of a node. EndedAt stays raw: absent and null differ.
type foldedNode struct {
	StartedAt any             `json:"startedAt"`
	EndedAt   json.RawMessage `json:"endedAt"`
	Type      string          `json:"type"`
	Status    string          `json:"status"`
	Credits   []struct {
		Usage any             `json:"usage"`
		Unit  json.RawMessage `json:"unit"`
	} `json:"credits"`
}

func (w *passWindow) add(n *indexNode, trail []string, parentType string) {
	path := append(append(make([]string, 0, len(trail)+1), trail...), segmentOf(n.nodeID, n.iteration, parentType))
	var f foldedNode
	if json.Unmarshal(n.own, &f) == nil {
		w.addCredits(&f)
		if f.Type == "step" || f.Type == "watch" {
			w.addWork(&f, PathKey(path))
		}
	}
	for _, k := range n.children {
		w.add(k, path, n.kind)
	}
}

// addCredits counts the node's total as the client's creditTotal does: a finite usage with no
// unit or the credit unit, and only a positive total.
func (w *passWindow) addCredits(f *foldedNode) {
	var total float64
	for _, c := range f.Credits {
		usage, ok := c.Usage.(float64)
		if !ok || math.IsInf(usage, 0) || math.IsNaN(usage) {
			continue
		}
		if len(c.Unit) == 0 || string(c.Unit) == `"credit"` {
			total += usage
		}
	}
	if total > 0 {
		w.credits += total
	}
}

func (w *passWindow) addWork(f *foldedNode, key string) {
	kas, ok := f.StartedAt.(string)
	if !ok {
		return
	}
	start := kas
	if log, held := w.starts[key]; held && (!log.Ended || !nodeInFlight(f.Status)) {
		start = log.StartedAt
	}
	s, ok := unixMilli(start)
	if !ok {
		return
	}
	if w.from == 0 || s < w.from {
		w.from = s
	}
	if len(f.EndedAt) == 0 {
		w.open = true
		return
	}
	var end string
	_ = json.Unmarshal(f.EndedAt, &end)
	if e, parsed := unixMilli(end); parsed {
		w.to = max(w.to, e)
	} else {
		w.to = max(w.to, s)
	}
}

func (w *passWindow) summary(count int) *EarlierPasses {
	return &EarlierPasses{Count: count, Credits: w.credits, From: w.from, To: w.to, Open: w.open}
}

// nodeInFlight is the run store's nodeInFlight: every status but a settled one, an unknown one
// included.
func nodeInFlight(status string) bool {
	switch status {
	case "completed", "failed", "aborted", "skipped":
		return false
	}
	return true
}

func unixMilli(stamp string) (int64, bool) {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}
