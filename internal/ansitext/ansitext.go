// Package ansitext separates ANSI escape sequences from the text they style.
//
// Agent command output renders in the chat transcript, which needs it searchable and exportable as text, passed
// through the sanitizers as text, and never turned into an HTML string. So the parse happens once on the server and
// yields plain text plus style spans addressing ranges of it by offset; the common case (99.75% of real tool outputs
// have no escape) costs an empty slice.
//
// It is a linear parser, not a terminal: agent terminals are pipes with no PTY, so grid-only sequences (cursor
// movement, erases, scroll regions, every non-SGR CSI final) are dropped. SGR covers all ten attributes, the 16 and
// 8 bright colours, the 256-colour palette and truecolour.
//
// Attribute bits match web-terminal-engine's vt.WireRun.A so one client table serves the shell and the transcript.
// Colours do not: spans are persisted, so they keep the palette index (0-255) for the user's CSS theme to resolve
// rather than baking today's 0xRRGGBB into the record.
package ansitext

import (
	"strings"
	"unicode/utf8"
)

// Style attribute bits, matching web-terminal-engine's vt.WireRun.A. 1024 is WireRun's AttrAutolink.
const (
	AttrBold uint16 = 1 << iota
	AttrItalic
	AttrUnderline
	AttrInverse
	AttrStrike
	AttrDim
	AttrHidden
	AttrBlink
	AttrOverline
	AttrDoubleUnderline
)

// ColorDefault marks "no colour set", distinct from black (index 0).
const ColorDefault int32 = -1

// rgbFlag marks an FG/BG value as packed 24-bit colour; one past the largest 24-bit value, so the spaces cannot
// collide.
const rgbFlag int32 = 0x1000000

// Span styles the half-open range [Start,End) of the plain text. Offsets are UTF-16 code units because the consumer
// indexes with JavaScript string offsets. A span is emitted only when some field is non-default.
type Span struct {
	// Start is the inclusive UTF-16 offset into the plain text.
	Start int `json:"start"`
	// End is the exclusive UTF-16 offset into the plain text.
	End int `json:"end"`
	// FG is the foreground colour: ColorDefault, a 0-255 palette index, or rgbFlag|RGB.
	FG int32 `json:"fg"`
	// BG is the background colour, encoded like FG.
	BG int32 `json:"bg"`
	// Attrs is the OR of the Attr* bits.
	Attrs uint16 `json:"attrs"`
}

// RGB packs three components into an FG/BG value.
func RGB(r, g, b uint8) int32 {
	return rgbFlag | int32(r)<<16 | int32(g)<<8 | int32(b)
}

// maxPendingBytes bounds how long an unterminated escape may be held, so `ESC ]` plus gigabytes cannot grow the
// buffer. One 4 KB read chunk, far above any real sequence. Past it the run is released as text; only there can
// chunked and one-shot parsing disagree, so FuzzParse scopes streaming agreement to inputs within the bound.
const maxPendingBytes = 4096

// utf16Len counts a string's UTF-16 code units: one per rune below U+10000, two above. An invalid byte is U+FFFD,
// one unit, as the browser receives it after JSON.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

// neutralizeESC replaces every ESC byte with U+FFFD on the paths that release unparsed bytes (an overlong run, a
// stream ending mid-sequence), which would otherwise persist a raw ESC. With hidden Unicode sanitized first and
// every other family consumed by the parser, the output is escape-free by construction (FuzzParse asserts it), so
// this replaces sanitize.StripANSI. One U+FFFD per ESC keeps UTF-16 length additive.
func neutralizeESC(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	return strings.ReplaceAll(s, "\x1b", "\uFFFD")
}

// validUTF8PerByte replaces each invalid byte with U+FFFD, so fragment lengths add up to the concatenation's;
// strings.ToValidUTF8 collapses runs and breaks that. Valid input is returned unchanged.
func validUTF8PerByte(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			sb.WriteRune(utf8.RuneError)
			i++
			continue
		}
		sb.WriteString(s[i : i+size])
		i += size
	}
	return sb.String()
}

// incompleteRuneTail returns how many trailing bytes of b begin a rune whose continuation has not arrived: 0 for a
// complete or empty tail or a byte that can never complete one, at most 3. utf8.FullRuneInString counts an invalid
// lead (0xC0, 0xC1, 0xF5-0xF7) as full, so such a tail is not held for a continuation that cannot come.
func incompleteRuneTail(b string) int {
	for back := 1; back <= utf8.UTFMax-1 && back <= len(b); back++ {
		if utf8.RuneStart(b[len(b)-back]) {
			if !utf8.FullRuneInString(b[len(b)-back:]) {
				return back
			}
			return 0
		}
	}
	return 0
}

// style is the parser's current SGR state.
type style struct {
	fg    int32
	bg    int32
	attrs uint16
}

func (s style) isDefault() bool {
	return s.fg == ColorDefault && s.bg == ColorDefault && s.attrs == 0
}

// Parser holds the SGR state and the incomplete-sequence remainder between Write calls, so an escape split across
// chunks does not leak and a colour carries over. Use one Parser per output stream. Not safe for concurrent use.
type Parser struct {
	// pending holds an incomplete trailing escape or rune; longer than maxPendingBytes it is released as text.
	pending []byte
	cur     style
	// offset is the plain-text length emitted so far in UTF-16 units, so spans across Writes address one document.
	offset int
}

// NewParser returns a Parser with default style and no pending bytes.
func NewParser() *Parser { return &Parser{cur: style{fg: ColorDefault, bg: ColorDefault}} }

// Offset returns how many UTF-16 units this Parser has emitted, the offset the next unit carries. The agent's
// terminal_output reads it rather than keeping a second counter that could drift from the span offsets.
func (p *Parser) Offset() int { return p.offset }

// Write consumes one chunk and returns its plain text and spans. Span offsets are absolute across the Parser's
// lifetime. Text and spans may each be empty.
func (p *Parser) Write(chunk string) (text string, spans []Span) {
	buf := chunk
	if len(p.pending) > 0 {
		buf = string(p.pending) + chunk
		p.pending = p.pending[:0]
	}
	w := &writer{p: p, openStart: p.offset, openStyle: p.cur}
	w.sb.Grow(len(buf))
	w.scan(buf)

	text = w.sb.String()
	p.offset += w.emitted
	w.closeRun(p.offset)
	return text, w.spans
}

// Flush releases any held incomplete sequence as literal text; call it once at end of stream so a truncated escape
// is shown rather than dropped.
func (p *Parser) Flush() (text string, spans []Span) {
	if len(p.pending) == 0 {
		return "", nil
	}
	text = validUTF8PerByte(neutralizeESC(string(p.pending)))
	p.pending = p.pending[:0]
	start := p.offset
	p.offset += utf16Len(text)
	if !p.cur.isDefault() {
		spans = []Span{{Start: start, End: p.offset, FG: p.cur.fg, BG: p.cur.bg, Attrs: p.cur.attrs}}
	}
	return text, spans
}

// Parse is the one-shot form for a complete string.
func Parse(s string) (text string, spans []Span) {
	p := NewParser()
	text, spans = p.Write(s)
	tailText, tailSpans := p.Flush()
	if tailText != "" {
		text += tailText
		spans = append(spans, tailSpans...)
	}
	return text, spans
}

// writer carries one Write call's state: the text so far, its UTF-16 length and the open style run.
type writer struct {
	p         *Parser
	spans     []Span
	sb        strings.Builder
	emitted   int
	openStart int
	openStyle style
}

// write appends text, coerced to valid UTF-8 per byte. An escape between a rune's bytes or a chunk boundary mid-rune
// would otherwise make offsets disagree with the client's string; per-run replacement breaks length additivity (both
// found by FuzzParse).
func (w *writer) write(s string) {
	valid := validUTF8PerByte(s)
	w.sb.WriteString(valid)
	w.emitted += utf16Len(valid)
}

// closeRun emits the open style run, ending at the given absolute offset.
func (w *writer) closeRun(absEnd int) {
	if absEnd > w.openStart && !w.openStyle.isDefault() {
		w.spans = append(w.spans, Span{
			Start: w.openStart, End: absEnd,
			FG: w.openStyle.fg, BG: w.openStyle.bg, Attrs: w.openStyle.attrs,
		})
	}
}

// scan consumes buf, writing its text and recording style runs.
func (w *writer) scan(buf string) {
	for i := 0; i < len(buf); {
		e := strings.IndexByte(buf[i:], 0x1b)
		if e < 0 {
			w.writeTail(buf[i:])
			return
		}
		if e > 0 {
			w.write(buf[i : i+e])
		}
		i += e
		// buf[i] is ESC.
		consumed, newStyle, changed, incomplete := w.p.scanEscape(buf[i:])
		if incomplete {
			w.hold(buf[i:])
			return
		}
		if changed {
			w.restyle(newStyle)
		}
		i += consumed
	}
}

// writeTail writes the final plain run, holding an incomplete trailing rune for the next Write so a split character
// is not two replacements. Owning this here makes the parser correct under any split (FuzzParse).
func (w *writer) writeTail(tail string) {
	if hold := incompleteRuneTail(tail); hold > 0 {
		w.p.pending = append(w.p.pending[:0], tail[len(tail)-hold:]...)
		tail = tail[:len(tail)-hold]
	}
	w.write(tail)
}

// hold keeps an unterminated escape for the next Write, or releases it as text past maxPendingBytes.
func (w *writer) hold(rest string) {
	if len(rest) > maxPendingBytes {
		w.write(neutralizeESC(rest))
		return
	}
	w.p.pending = append(w.p.pending[:0], rest...)
}

// restyle closes the open run at the current position and opens a new one.
func (w *writer) restyle(s style) {
	absEnd := w.p.offset + w.emitted
	w.closeRun(absEnd)
	w.openStart = absEnd
	w.openStyle = s
	w.p.cur = s
}

// scanEscape examines an escape sequence starting at b[0]==ESC. consumed is its length (0 when incomplete);
// changed reports a style change, given in newStyle; incomplete means wait for more bytes.
func (p *Parser) scanEscape(b string) (consumed int, newStyle style, changed, incomplete bool) {
	if len(b) < 2 {
		return 0, p.cur, false, true
	}
	switch b[1] {
	case '[':
		return p.scanCSI(b)
	case ']':
		n := scanStringTerminated(b)
		return n, p.cur, false, n == 0
	case '(', ')', '*', '+', '-', '.', '/', '#', '%':
		// Three-byte forms (ESC ( B, ESC - A, ESC # 8, ESC % G) are grid-only and consumed whole, or the final byte leaks.
		if len(b) < 3 {
			return 0, p.cur, false, true
		}
		return 3, p.cur, false, false
	case 'P', 'X', '^', '_':
		// DCS / SOS / PM / APC, terminated by ST. Dropped whole.
		n := scanStringTerminated(b)
		return n, p.cur, false, n == 0
	default:
		// A lone ESC or a two-byte sequence (ESC c, ESC 7, ...). Dropped.
		return 2, p.cur, false, false
	}
}

// scanCSI parses ESC [ ... <final>; only SGR ('m') changes style, every other final is a dropped grid operation.
func (p *Parser) scanCSI(b string) (consumed int, newStyle style, changed, incomplete bool) {
	i := 2
	for i < len(b) {
		c := b[i]
		// Parameter bytes 0x30-0x3f, intermediate bytes 0x20-0x2f.
		if (c >= 0x30 && c <= 0x3f) || (c >= 0x20 && c <= 0x2f) {
			i++
			continue
		}
		if c >= 0x40 && c <= 0x7e {
			if c != 'm' {
				return i + 1, p.cur, false, false
			}
			st := applySGR(p.cur, b[2:i])
			return i + 1, st, st != p.cur, false
		}
		// An illegal byte inside a CSI: consume what we have so the stream resynchronizes.
		return i, p.cur, false, false
	}
	return 0, p.cur, false, true
}

// scanStringTerminated parses an OSC/DCS/SOS/PM/APC string ending in ST or BEL, returning 0 when incomplete. OSC 8
// hyperlinks are dropped: an agent-supplied href is unvalidated.
func scanStringTerminated(b string) int {
	for i := 2; i < len(b); i++ {
		if b[i] == 0x07 {
			return i + 1
		}
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
			return i + 2
		}
	}
	return 0
}

// applySGR folds one SGR parameter list into a style; an empty list is a reset, like `ESC[m`.
func applySGR(cur style, params string) style {
	if params == "" {
		return style{fg: ColorDefault, bg: ColorDefault}
	}
	if isPrivateParams(params) {
		return cur
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		n, ok := sgrParam(fields[i])
		if !ok {
			// Not a parameter (an intermediate byte scanCSI swept up): skipped, since 0 is a reset.
			continue
		}
		// 38 and 48 consume the parameters after them, so they advance the index.
		if n == 38 || n == 48 {
			cur, i = applyExtendedColor(cur, n, fields, i)
			continue
		}
		cur = applySGRParam(cur, n)
	}
	return cur
}

// isPrivateParams reports whether SGR parameters open with a private marker (`<`, `=`, `>`, `?`), making the
// sequence private-mode, like xterm's `ESC[>4;2m`. Read as SGR, `>4` would be a 0 field and reset every attribute.
func isPrivateParams(params string) bool {
	return params[0] >= 0x3c && params[0] <= 0x3f
}

// applyExtendedColor folds a 38/48 selector and its parameters into a style, returning the caller's advanced index.
// An invalid colour still advances so it cannot swallow the styling after it.
func applyExtendedColor(cur style, selector int, fields []string, i int) (next style, advanced int) {
	c, adv, valid := extendedColor(fields[i+1:])
	if !valid {
		return cur, i + adv
	}
	if selector == 38 {
		cur.fg = c
	} else {
		cur.bg = c
	}
	return cur, i + adv
}

// applySGRParam folds one self-contained SGR parameter into a style.
func applySGRParam(cur style, n int) style {
	if set, ok := sgrAttrSet[n]; ok {
		cur.attrs |= set
		return cur
	}
	if off, ok := sgrAttrClear[n]; ok {
		cur.attrs &^= off
		return cur
	}
	switch {
	case n == 0:
		return style{fg: ColorDefault, bg: ColorDefault}
	case n == 39:
		cur.fg = ColorDefault
	case n == 49:
		cur.bg = ColorDefault
	case n >= 30 && n <= 37:
		cur.fg = int32(n - 30)
	case n >= 40 && n <= 47:
		cur.bg = int32(n - 40)
	case n >= 90 && n <= 97:
		cur.fg = int32(n - 90 + 8)
	case n >= 100 && n <= 107:
		cur.bg = int32(n - 100 + 8)
	}
	return cur
}

// sgrAttrSet maps an SGR parameter to the attribute bits it sets. 6 (rapid blink) renders like 5.
var sgrAttrSet = map[int]uint16{
	1:  AttrBold,
	2:  AttrDim,
	3:  AttrItalic,
	4:  AttrUnderline,
	5:  AttrBlink,
	6:  AttrBlink,
	7:  AttrInverse,
	8:  AttrHidden,
	9:  AttrStrike,
	21: AttrDoubleUnderline,
	53: AttrOverline,
}

// sgrAttrClear maps an SGR parameter to the bits it clears: 22 clears bold and dim, 24 both underlines, so it is a
// mask per entry, not sgrAttrSet's inverse.
var sgrAttrClear = map[int]uint16{
	22: AttrBold | AttrDim,
	23: AttrItalic,
	24: AttrUnderline | AttrDoubleUnderline,
	25: AttrBlink,
	27: AttrInverse,
	28: AttrHidden,
	29: AttrStrike,
	55: AttrOverline,
}

// extendedColor reads the parameters after a 38/48 selector (`5;<idx>` or `2;<r>;<g>;<b>`). adv is how many it
// consumed, so a malformed colour cannot swallow the styling after it.
func extendedColor(rest []string) (c int32, adv int, ok bool) {
	if len(rest) == 0 {
		return 0, 0, false
	}
	mode, valid := sgrParam(rest[0])
	if !valid {
		return 0, 1, false
	}
	switch mode {
	case 5:
		return palette256(rest)
	case 2:
		return trueColor(rest)
	default:
		return 0, 1, false
	}
}

// palette256 reads `5;<idx>`.
func palette256(rest []string) (c int32, adv int, ok bool) {
	if len(rest) < 2 {
		return 0, 1, false
	}
	idx, valid := sgrParam(rest[1])
	if !valid || idx < 0 || idx > 255 {
		return 0, 2, false
	}
	return int32(idx), 2, true
}

// trueColor reads `2;<r>;<g>;<b>`.
func trueColor(rest []string) (c int32, adv int, ok bool) {
	if len(rest) < 4 {
		return 0, len(rest), false
	}
	var comp [3]uint8
	for i := range comp {
		v, valid := sgrParam(rest[i+1])
		if !valid || v < 0 || v > 255 {
			return 0, 4, false
		}
		// #nosec G115 -- bounded to 0-255 on the line above.
		comp[i] = uint8(v)
	}
	return RGB(comp[0], comp[1], comp[2]), 4, true
}

// sgrParam parses one decimal SGR parameter field. An empty field is 0, as terminals read it. Colon subparameters
// are dropped (`4:3` curly underline, `58:2::1:2:3` underline colour): read whole, the field is non-numeric, and
// `ESC[4:3m`, which gcc and clang emit, would reset every attribute. ok is false otherwise and the caller skips the
// field. The value is bounded against overflow.
func sgrParam(field string) (n int, ok bool) {
	if base, _, found := strings.Cut(field, ":"); found {
		field = base
	}
	if field == "" {
		return 0, true
	}
	for i := range len(field) {
		if field[i] < '0' || field[i] > '9' {
			return 0, false
		}
		n = n*10 + int(field[i]-'0')
		if n > 0xffff {
			return 0, false
		}
	}
	return n, true
}
