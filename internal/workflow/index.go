package workflow

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"strconv"
)

// ReadIndex is one inspect reply taken apart for a revision diff: each `state` member but `root`
// in the reply's order, each node of `state.root` by its PathKey, and `nodePlan`. Values are the
// reply's own bytes.
type ReadIndex struct {
	Nodes map[string]IndexedNode
	Plan  json.RawMessage
	Head  []Member
}

// Member is one object member as the reply spelled it.
type Member struct {
	Name  string
	Value json.RawMessage
}

// IndexedNode is one state node: its members other than `children`, as one JSON object, and where
// it sits (its parent's PathKey, "" for the root, and its index among the parent's children).
type IndexedNode struct {
	Parent string
	Own    json.RawMessage
	Index  int
}

// indexLenient reads KAS's bytes as encoding/json does: a duplicate name or invalid UTF-8 is
// passed on, not refused.
var indexLenient = []jsontext.Options{jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)}

// IndexRead indexes an inspect reply in one pass. A reply whose top level, `state` or a node is
// not an object is an error; a non-object child is skipped.
func IndexRead(raw []byte) (ReadIndex, error) {
	ix := ReadIndex{Nodes: map[string]IndexedNode{}}
	dec := jsontext.NewDecoder(bytes.NewReader(raw), indexLenient...)
	var root *indexNode
	err := eachMember(dec, func(name string) error {
		switch name {
		case "state":
			return eachMember(dec, func(member string) error {
				if member == "root" {
					n, err := readIndexNode(dec)
					root = n
					return err
				}
				v, err := dec.ReadValue()
				ix.Head = append(ix.Head, Member{Name: member, Value: v.Clone()})
				return err
			})
		case "nodePlan":
			v, err := dec.ReadValue()
			ix.Plan = v.Clone()
			return err
		}
		return dec.SkipValue()
	})
	if err != nil {
		return ReadIndex{}, fmt.Errorf("index inspect: %w", err)
	}
	if root != nil {
		root.index(&ix, nil, "", "", 0)
	}
	return ix, nil
}

// indexNode is a node as read, before its path is known: a node's path needs its parent's type
// and its own iteration, which may follow its children in the bytes.
type indexNode struct {
	iteration *int
	nodeID    string
	kind      string
	own       []byte
	children  []*indexNode
	// listed is whether the node has a `children` array, an empty one included.
	listed bool
}

func (n *indexNode) index(ix *ReadIndex, trail []string, parentKey, parentType string, at int) {
	path := make([]string, 0, len(trail)+1)
	path = append(path, trail...)
	path = append(path, segmentOf(n.nodeID, n.iteration, parentType))
	key := PathKey(path)
	ix.Nodes[key] = IndexedNode{Own: n.own, Parent: parentKey, Index: at}
	for i, k := range n.children {
		k.index(ix, path, key, n.kind, i)
	}
}

func readIndexNode(dec *jsontext.Decoder) (*indexNode, error) {
	n := &indexNode{}
	own := []byte{'{'}
	err := eachMember(dec, func(name string) error {
		if name == "children" && dec.PeekKind() == '[' {
			return n.readChildren(dec)
		}
		v, err := dec.ReadValue()
		if err != nil {
			return err
		}
		if len(own) > 1 {
			own = append(own, ',')
		}
		own, _ = jsontext.AppendQuote(own, name)
		own = append(own, ':')
		own = append(own, v...)
		n.member(name, v)
		return nil
	})
	own = append(own, '}')
	n.own = own
	return n, err
}

// member keeps the fields a node's path is made of.
func (n *indexNode) member(name string, v jsontext.Value) {
	switch name {
	case "nodeId", "type":
		s, err := jsontext.AppendUnquote(nil, v)
		if err != nil || v.Kind() != '"' {
			return
		}
		if name == "nodeId" {
			n.nodeID = string(s)
		} else {
			n.kind = string(s)
		}
	case "iteration":
		if i, err := strconv.Atoi(string(v)); err == nil {
			n.iteration = &i
		}
	}
}

func (n *indexNode) readChildren(dec *jsontext.Decoder) error {
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	n.listed = true
	for dec.PeekKind() != ']' {
		if dec.PeekKind() != '{' {
			if err := dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		k, err := readIndexNode(dec)
		if err != nil {
			return err
		}
		n.children = append(n.children, k)
	}
	_, err := dec.ReadToken()
	return err
}

// eachMember reads the object at dec, handing each member's name to read, which must consume its
// value.
func eachMember(dec *jsontext.Decoder, read func(name string) error) error {
	if kind := dec.PeekKind(); kind != '{' {
		return fmt.Errorf("want an object, have %v", kind)
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		if err := read(tok.String()); err != nil {
			return err
		}
	}
	_, err := dec.ReadToken()
	return err
}
