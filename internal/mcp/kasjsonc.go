package mcp

// KAS reads its mcp.json with comment-json (https://github.com/kaelzhang/node-comment-json): `//`
// and `/* */` comments, and a trailing comma in an object or array, which is the JWCC dialect
// hujson parses (https://nigeltao.github.io/blog/2021/json-with-commas-comments.html). marotte
// keeps the file as hujson's lossless syntax tree and edits only its own members in it, so a
// write keeps every other byte of a hand-edited file.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/cplieger/marotte/internal/userjson"
	"github.com/tailscale/hujson"
)

// kasMaxDepth is encoding/json's nesting limit (maxNestingDepth in
// https://go.dev/src/encoding/json/scanner.go). hujson recurses once per level with no limit of its
// own (https://github.com/tailscale/hujson/blob/b80ff77dac4f/parse.go), and a deep enough file
// overflows the goroutine stack, a fatal error no recover catches.
const kasMaxDepth = 10000

const kasIndent = "  "

// kasDoc is KAS's config file as read: members holds each top-level member as KAS reads it (the
// last of a duplicated name), and src the file's syntax tree after any UTF-8 BOM, which a write edits.
type kasDoc struct {
	members map[string]json.RawMessage
	src     hujson.Value
}

// parseKASDoc reads data as KAS does. fresh reports content with no document (whitespace or a
// JSON null); a non-object document, or one deeper than kasMaxDepth, is an error.
func parseKASDoc(data []byte) (doc *kasDoc, fresh bool, err error) {
	body := userjson.Body(data)
	doc = &kasDoc{members: map[string]json.RawMessage{}}
	if userjson.Empty(body) {
		return doc, true, nil
	}
	if dErr := checkKASDepth(body); dErr != nil {
		return nil, false, dErr
	}
	src, err := hujson.Parse(body)
	if err != nil && !bytes.HasSuffix(body, []byte("\n")) {
		// comment-json ends a line comment at the end of the file; hujson needs its newline.
		if retried, rErr := hujson.Parse(append(slices.Clip(body), '\n')); rErr == nil {
			src, err = retried, nil
		}
	}
	if err != nil {
		return nil, false, err
	}
	std := src.Clone()
	std.Standardize()
	var members map[string]json.RawMessage
	if uErr := json.Unmarshal(std.Pack(), &members); uErr != nil {
		return nil, false, uErr
	}
	doc.src = src
	if members == nil {
		return doc, true, nil
	}
	doc.members = members
	return doc, false, nil
}

// checkKASDepth refuses nesting past kasMaxDepth, counting brackets outside strings and comments.
func checkKASDepth(b []byte) error {
	depth := 0
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			for i++; i < len(b) && b[i] != '"'; i++ {
				if b[i] == '\\' {
					i++
				}
			}
		case '/':
			i += commentLen(b[i:])
		case '{', '[':
			depth++
			if depth > kasMaxDepth {
				return fmt.Errorf("nesting deeper than %d levels", kasMaxDepth)
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

// commentLen is the length, less one, of the comment b starts with; an unterminated one runs to the
// end of b, and anything else is 0.
func commentLen(b []byte) int {
	var end []byte
	switch {
	case bytes.HasPrefix(b, []byte("//")):
		end = []byte("\n")
	case bytes.HasPrefix(b, []byte("/*")):
		end = []byte("*/")
	default:
		return 0
	}
	if j := bytes.Index(b[2:], end); j >= 0 {
		return 2 + j + len(end) - 1
	}
	return len(b) - 1
}

// render is the file with marotte's two keys set from members (an absent one removed) and every
// other byte as read. It edits src, so a doc renders once.
func (d *kasDoc) render() ([]byte, error) {
	root := &d.src
	if _, ok := root.Value.(*hujson.Object); !ok {
		root.Value = &hujson.Object{}
	}
	for _, key := range []string{kasServerKey, kasPowersKey} {
		want, ok := d.members[key]
		if !ok {
			removeMembers(root, key)
			continue
		}
		if err := setMember(root, key, want, ""); err != nil {
			return nil, err
		}
	}
	return root.Pack(), nil
}

func memberName(m *hujson.ObjectMember) string {
	lit, ok := m.Name.Value.(hujson.Literal)
	if !ok {
		return ""
	}
	return lit.String()
}

// setMember makes the last member of obj named name hold want, or appends one: KAS reads the last
// of a duplicated name, so an earlier one is left as it is. indent is obj's own line indentation.
func setMember(obj *hujson.Value, name string, want json.RawMessage, indent string) error {
	o, ok := obj.Value.(*hujson.Object)
	if !ok {
		return fmt.Errorf("%w kas mcp.json: a member's parent is not an object", errPersistMarshal)
	}
	for i := len(o.Members) - 1; i >= 0; i-- {
		m := &o.Members[i]
		if memberName(m) == name {
			return patchValue(&m.Value, want, lineIndent(m.Name.BeforeExtra, indent+kasIndent))
		}
	}
	return appendMember(o, name, want, indent)
}

// patchValue makes v hold want. A value that already decodes to want keeps its bytes, an object is
// patched member by member so the comments around its unchanged members survive, and anything else
// is replaced, comments inside it included, keeping a one-line object or array on one line. indent is v's line indentation.
func patchValue(v *hujson.Value, want json.RawMessage, indent string) error {
	have := v.Clone()
	have.Standardize()
	if sameJSONValue(have.Pack(), want) {
		return nil
	}
	if _, isObject := v.Value.(*hujson.Object); isObject {
		var wantMembers map[string]json.RawMessage
		if json.Unmarshal(want, &wantMembers) == nil && wantMembers != nil {
			return patchObject(v, wantMembers, indent)
		}
	}
	composite := v.Value.Kind() == '{' || v.Value.Kind() == '['
	replaced, err := parseRendered(want, indent, composite && onOneLine(v.Value))
	if err != nil {
		return err
	}
	v.Value = replaced
	return nil
}

func patchObject(v *hujson.Value, want map[string]json.RawMessage, indent string) error {
	o, ok := v.Value.(*hujson.Object)
	if !ok {
		return fmt.Errorf("%w kas mcp.json: patching a non-object as an object", errPersistMarshal)
	}
	for i := len(o.Members) - 1; i >= 0; i-- {
		if _, keep := want[memberName(&o.Members[i])]; !keep {
			removeMemberAt(o, i)
		}
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := setMember(v, name, want[name], indent); err != nil {
			return err
		}
	}
	return nil
}

func removeMembers(obj *hujson.Value, name string) {
	o, ok := obj.Value.(*hujson.Object)
	if !ok {
		return
	}
	for i := len(o.Members) - 1; i >= 0; i-- {
		if memberName(&o.Members[i]) == name {
			removeMemberAt(o, i)
		}
	}
}

// removeMemberAt drops member i with the comments before it and the one trailing it on its line,
// keeping a comment on the previous member's line with that member.
func removeMemberAt(o *hujson.Object, i int) {
	kept := sameLineExtra(o.Members[i].Name.BeforeExtra)
	if hujson.Extra(kept).IsStandard() {
		kept = nil
	}
	next := &o.AfterExtra
	if i+1 < len(o.Members) {
		next = &o.Members[i+1].Name.BeforeExtra
	}
	*next = append(slices.Clip(kept), (*next)[len(sameLineExtra(*next)):]...)
	o.Members = slices.Delete(o.Members, i, i+1)
}

// appendMember adds name last in o, after the comment that trails the current last member on its
// line; an object written on one line stays on one line. indent is o's own line indentation.
func appendMember(o *hujson.Object, name string, want json.RawMessage, indent string) error {
	oneLine := len(o.Members) > 0 && onOneLine(o)
	memberIndent := indent + kasIndent
	if n := len(o.Members); n > 0 {
		memberIndent = lineIndent(o.Members[n-1].Name.BeforeExtra, memberIndent)
	}
	value, err := parseRendered(want, memberIndent, oneLine)
	if err != nil {
		return err
	}
	trailing := sameLineExtra(o.AfterExtra)
	rest := o.AfterExtra[len(trailing):]
	lead := slices.Clip(bytes.TrimRight(trailing, " \t"))
	switch {
	case oneLine:
		lead = append(lead, ' ')
	default:
		lead = append(lead, "\n"+memberIndent...)
		if !bytes.Contains(rest, []byte("\n")) {
			rest = append([]byte("\n"+indent), bytes.TrimLeft(rest, " \t")...)
		}
	}
	o.AfterExtra = rest
	o.Members = append(o.Members, hujson.ObjectMember{
		Name:  hujson.Value{BeforeExtra: lead, Value: hujson.String(name)},
		Value: hujson.Value{BeforeExtra: hujson.Extra(" "), Value: value},
	})
	return nil
}

// onOneLine reports whether v's text, comments inside it included, holds no newline.
func onOneLine(v hujson.ValueTrimmed) bool {
	return !bytes.Contains(hujson.Value{Value: v}.Pack(), []byte("\n"))
}

// parseRendered renders want as marotte writes it on a line indented by indent, or compact.
func parseRendered(want json.RawMessage, indent string, oneLine bool) (hujson.ValueTrimmed, error) {
	var rendered []byte
	var err error
	if oneLine {
		rendered, err = json.Marshal(want)
	} else {
		rendered, err = json.MarshalIndent(want, indent, kasIndent)
	}
	if err != nil {
		return nil, fmt.Errorf("%w kas mcp.json: %w", errPersistMarshal, err)
	}
	v, err := hujson.Parse(rendered)
	if err != nil {
		return nil, fmt.Errorf("%w kas mcp.json: %w", errPersistMarshal, err)
	}
	return v.Value, nil
}

// lineIndent is the indentation b leaves before the value that follows it: what follows its last
// newline, or fallback when b ends on a line holding anything else.
func lineIndent(b []byte, fallback string) string {
	i := bytes.LastIndexByte(b, '\n')
	if i < 0 || len(bytes.Trim(b[i+1:], " \t")) > 0 {
		return fallback
	}
	return string(b[i+1:])
}

// sameLineExtra is the part of b before its first newline outside a comment: what trails the
// previous value on its line.
func sameLineExtra(b []byte) []byte {
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '\n':
			return b[:i]
		case bytes.HasPrefix(b[i:], []byte("//")):
			if j := bytes.IndexByte(b[i:], '\n'); j >= 0 {
				return b[:i+j]
			}
			return b
		case b[i] == '/':
			i += commentLen(b[i:])
		}
	}
	return b
}
