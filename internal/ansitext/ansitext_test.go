package ansitext

import (
	"strconv"
	"strings"
	"testing"
)

// styleAt returns the style covering off, or the zero style; spans never overlap.
func styleAt(spans []Span, off int) (Span, bool) {
	for _, s := range spans {
		if off >= s.Start && off < s.End {
			return s, true
		}
	}
	return Span{}, false
}

func TestParse_PlainTextProducesNoSpans(t *testing.T) {
	const in = "hello world\nsecond line\n"
	text, spans := Parse(in)
	if text != in {
		t.Errorf("text = %q, want %q", text, in)
	}
	if len(spans) != 0 {
		t.Errorf("got %d spans for unstyled text, want none", len(spans))
	}
}

func TestParse_StripsSequencesFromTheText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Shapes from real tool output: gitleaks' zerolog writer and hadolint colour unconditionally.
		{name: "sgr colour", in: "\x1b[90m1:47AM\x1b[0m \x1b[32mINF\x1b[0m ok", want: "1:47AM INF ok"},
		{name: "bright fg", in: "\x1b[92minfo\x1b[0m", want: "info"},
		{name: "reset shorthand", in: "\x1b[1mbold\x1b[mplain", want: "boldplain"},
		// Grid operations are dropped: a pipe has no cursor or screen.
		{name: "cursor move", in: "a\x1b[2Ab", want: "ab"},
		{name: "erase line", in: "a\x1b[2Kb", want: "ab"},
		{name: "erase display", in: "a\x1b[3Jb", want: "ab"},
		{name: "scroll region", in: "a\x1b[1;24rb", want: "ab"},
		{name: "private mode", in: "a\x1b[?25lb", want: "ab"},
		// Other escape families.
		{name: "osc bel terminated", in: "a\x1b]0;title\x07b", want: "ab"},
		{name: "osc st terminated", in: "a\x1b]8;;http://x\x1b\\b", want: "ab"},
		{name: "charset designation", in: "a\x1b(Bb", want: "ab"},
		{name: "dcs", in: "a\x1bPq~~\x1b\\b", want: "ab"},
		{name: "two byte escape", in: "a\x1bcb", want: "ab"},
		{name: "lone escape at end becomes U+FFFD", in: "ab\x1b", want: "ab\ufffd"},
		// A sequence ending exactly at the stream's end is complete and dropped, one comparison from the held case above.
		{name: "two byte escape ends the stream", in: "a\x1bc", want: "a"},
		{name: "charset designation ends the stream", in: "a\x1b(B", want: "a"},
		{name: "utf8 charset selection ends the stream", in: "a\x1b%G", want: "a"},
		// The ECMA-48 CSI byte ranges at their edges; a missed byte ends the sequence early and leaks the rest.
		{name: "csi intermediate byte space", in: "a\x1b[2 pb", want: "ab"},
		{name: "csi intermediate byte solidus", in: "a\x1b[1/pb", want: "ab"},
		{name: "csi final byte at 0x40", in: "a\x1b[1@b", want: "ab"},
		{name: "csi final byte at 0x7e", in: "a\x1b[3~b", want: "ab"},
		// Three-byte forms whose final byte leaked as a stray capital when only two were consumed.
		{name: "utf8 charset selection", in: "a\x1b%Gb", want: "ab"},
		{name: "96 char set designation", in: "a\x1b-Ab", want: "ab"},
		{name: "line attribute", in: "a\x1b#8b", want: "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, _ := Parse(tc.in)
			if text != tc.want {
				t.Errorf("text = %q, want %q", text, tc.want)
			}
			if strings.ContainsRune(text, 0x1b) {
				t.Errorf("text still contains ESC: %q", text)
			}
		})
	}
}

func TestParse_SpansAddressTheRightRanges(t *testing.T) {
	// "plain" unstyled, "red" in red, "tail" unstyled.
	text, spans := Parse("plain\x1b[31mred\x1b[0mtail")
	if text != "plainredtail" {
		t.Fatalf("text = %q", text)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1: %+v", len(spans), spans)
	}
	got := spans[0]
	if got.Start != 5 || got.End != 8 {
		t.Errorf("span range = [%d,%d), want [5,8) covering %q", got.Start, got.End, "red")
	}
	if text[got.Start:got.End] != "red" {
		t.Errorf("span covers %q, want %q", text[got.Start:got.End], "red")
	}
	if got.FG != 1 {
		t.Errorf("FG = %d, want 1 (red)", got.FG)
	}
	if _, ok := styleAt(spans, 0); ok {
		t.Error("offset 0 is styled, want the leading text unstyled")
	}
	if _, ok := styleAt(spans, 8); ok {
		t.Error("offset 8 is styled, want the trailing text unstyled")
	}
}

// Every SGR attribute. `ESC[7m` appears in real output and rendered unstyled before the server parse.
func TestApplySGR_AllAttributes(t *testing.T) {
	cases := []struct {
		name string
		seq  string
		want uint16
	}{
		{name: "bold", seq: "1", want: AttrBold},
		{name: "dim", seq: "2", want: AttrDim},
		{name: "italic", seq: "3", want: AttrItalic},
		{name: "underline", seq: "4", want: AttrUnderline},
		{name: "blink", seq: "5", want: AttrBlink},
		{name: "rapid blink", seq: "6", want: AttrBlink},
		{name: "inverse", seq: "7", want: AttrInverse},
		{name: "hidden", seq: "8", want: AttrHidden},
		{name: "strike", seq: "9", want: AttrStrike},
		{name: "double underline", seq: "21", want: AttrDoubleUnderline},
		{name: "overline", seq: "53", want: AttrOverline},
		{name: "bold and italic", seq: "1;3", want: AttrBold | AttrItalic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse("\x1b[" + tc.seq + "mx")
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}
			if spans[0].Attrs != tc.want {
				t.Errorf("attrs = %#b, want %#b", spans[0].Attrs, tc.want)
			}
		})
	}
}

// The bit values are a cross-language contract with web-terminal-engine's vt.WireRun.A and the client's one table
// (output-render.ts); an iota reorder would repaint bold as italic with every other test green.
func TestAttrBits_MatchTheWireRunContract(t *testing.T) {
	want := map[string]uint16{
		"bold": 1, "italic": 2, "underline": 4, "inverse": 8, "strike": 16,
		"dim": 32, "hidden": 64, "blink": 128, "overline": 256, "doubleUnderline": 512,
	}
	got := map[string]uint16{
		"bold": AttrBold, "italic": AttrItalic, "underline": AttrUnderline,
		"inverse": AttrInverse, "strike": AttrStrike, "dim": AttrDim,
		"hidden": AttrHidden, "blink": AttrBlink, "overline": AttrOverline,
		"doubleUnderline": AttrDoubleUnderline,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("Attr%s = %d, want %d (vt.WireRun.A contract)", name, got[name], w)
		}
	}
	// Bit 1024 is WireRun's AttrAutolink; nothing here may claim it.
	var all uint16
	for _, v := range got {
		all |= v
	}
	if all&1024 != 0 {
		t.Errorf("attribute bits = %#b, want none at 1024 (vt.AttrAutolink)", all)
	}
}

func TestApplySGR_AttributeOffSwitches(t *testing.T) {
	cases := []struct {
		name string
		seq  string
		want uint16
	}{
		{name: "22 clears bold and dim", seq: "1;2;22", want: 0},
		{name: "23 clears italic", seq: "3;23", want: 0},
		{name: "24 clears both underlines", seq: "4;21;24", want: 0},
		{name: "25 clears blink", seq: "5;25", want: 0},
		{name: "27 clears inverse", seq: "7;27", want: 0},
		{name: "28 clears hidden", seq: "8;28", want: 0},
		{name: "29 clears strike", seq: "9;29", want: 0},
		{name: "55 clears overline", seq: "53;55", want: 0},
		{name: "22 leaves italic alone", seq: "1;3;22", want: AttrItalic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse("\x1b[" + tc.seq + "mx")
			var got uint16
			if len(spans) == 1 {
				got = spans[0].Attrs
			}
			if got != tc.want {
				t.Errorf("attrs = %#b, want %#b", got, tc.want)
			}
		})
	}
}

func TestApplySGR_Colours(t *testing.T) {
	cases := []struct {
		name   string
		seq    string
		wantFG int32
		wantBG int32
	}{
		{name: "basic fg", seq: "31", wantFG: 1, wantBG: ColorDefault},
		{name: "basic bg", seq: "41", wantFG: ColorDefault, wantBG: 1},
		{name: "bright fg maps above 8", seq: "91", wantFG: 9, wantBG: ColorDefault},
		{name: "bright bg maps above 8", seq: "101", wantFG: ColorDefault, wantBG: 9},
		{name: "256 palette fg", seq: "38;5;208", wantFG: 208, wantBG: ColorDefault},
		{name: "256 palette bg", seq: "48;5;17", wantFG: ColorDefault, wantBG: 17},
		// Index 0 is black, a colour and not unset, and 255 is the last entry; excluding either paints the default.
		{name: "256 palette first index", seq: "38;5;0", wantFG: 0, wantBG: ColorDefault},
		{name: "256 palette last index", seq: "38;5;255", wantFG: 255, wantBG: ColorDefault},
		{name: "truecolour fg", seq: "38;2;10;20;30", wantFG: RGB(10, 20, 30), wantBG: ColorDefault},
		{name: "truecolour bg", seq: "48;2;1;2;3", wantFG: ColorDefault, wantBG: RGB(1, 2, 3)},
		{name: "truecolour component extremes", seq: "38;2;0;255;0", wantFG: RGB(0, 255, 0), wantBG: ColorDefault},
		{name: "truecolour white", seq: "48;2;255;255;255", wantFG: ColorDefault, wantBG: RGB(255, 255, 255)},
		{name: "39 resets fg only", seq: "31;41;39", wantFG: ColorDefault, wantBG: 1},
		{name: "49 resets bg only", seq: "31;41;49", wantFG: 1, wantBG: ColorDefault},
		{name: "fg and bg together", seq: "32;44", wantFG: 2, wantBG: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse("\x1b[" + tc.seq + "mx")
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}
			if spans[0].FG != tc.wantFG {
				t.Errorf("FG = %d, want %d", spans[0].FG, tc.wantFG)
			}
			if spans[0].BG != tc.wantBG {
				t.Errorf("BG = %d, want %d", spans[0].BG, tc.wantBG)
			}
		})
	}
}

func TestApplySGR_MalformedExtendedColourDoesNotCorruptLaterParams(t *testing.T) {
	// `38;5` without an index must not swallow the following bold.
	_, spans := Parse("\x1b[38;5m\x1b[1mx")
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Attrs&AttrBold == 0 {
		t.Errorf("attrs = %#b, want bold set", spans[0].Attrs)
	}
}

// An unhonourable parameter is ignored, never read as 0, which is a full reset. Each case is a real emitter.
func TestApplySGR_UnhonourableParametersDoNotReset(t *testing.T) {
	cases := []struct {
		name      string
		seq       string
		wantAttrs uint16
		wantFG    int32
		reason    string
	}{{
		name: "colon subparameter underline", seq: "\x1b[4:3mx",
		wantAttrs: AttrUnderline, wantFG: ColorDefault,
		reason: "gcc and clang emit ESC[4:3m for a curly diagnostic underline;" +
			" read whole the field is non-numeric, and non-numeric read as 0 resets",
	}, {
		name: "colon subparameter keeps earlier styling", seq: "\x1b[31m\x1b[4:3mx",
		wantAttrs: AttrUnderline, wantFG: 1,
		reason: "the red opened by the previous sequence must survive the underline",
	}, {
		name: "private parameter marker is ignored whole", seq: "\x1b[31m\x1b[>4;2mx",
		wantAttrs: 0, wantFG: 1,
		reason: "xterm's modifyOtherKeys has an SGR final byte but is not SGR;" +
			" terminals ignore it, and reading `>4` as 0 wiped the red",
	}, {
		name: "sgr mouse report is ignored whole", seq: "\x1b[31m\x1b[<0;1;1mx",
		wantAttrs: 0, wantFG: 1,
		reason: "an SGR mouse release report ends in `m` and is not SGR;" +
			" honouring its coordinates would bold the run on a click",
	}, {
		name: "dec private marker is ignored whole", seq: "\x1b[31m\x1b[?1;1mx",
		wantAttrs: 0, wantFG: 1,
		reason: "`?` opens a DEC private sequence, the last of the four markers",
	}, {
		name: "equals private marker is ignored whole", seq: "\x1b[31m\x1b[=1;1mx",
		wantAttrs: 0, wantFG: 1,
		reason: "`=` is a private marker too, so the parameters after it are not SGR",
	}, {
		name: "intermediate byte is skipped not zeroed", seq: "\x1b[1m\x1b[2 mx",
		wantAttrs: AttrBold, wantFG: ColorDefault,
		reason: "an intermediate byte means the sequence is not SGR at all",
	}, {
		name: "underline colour is dropped without resetting", seq: "\x1b[1m\x1b[58:2::1:2:3mx",
		wantAttrs: AttrBold, wantFG: ColorDefault,
		reason: "a Span carries no underline colour, so 58 is dropped — not read as a reset",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse(tc.seq)
			var gotAttrs uint16
			gotFG := ColorDefault
			if len(spans) > 0 {
				last := spans[len(spans)-1]
				gotAttrs, gotFG = last.Attrs, last.FG
			}
			if gotAttrs != tc.wantAttrs || gotFG != tc.wantFG {
				t.Errorf("attrs=%#b fg=%d, want attrs=%#b fg=%d (%s)",
					gotAttrs, gotFG, tc.wantAttrs, tc.wantFG, tc.reason)
			}
		})
	}
}

func TestParse_ResetClosesTheSpan(t *testing.T) {
	text, spans := Parse("\x1b[31mred\x1b[0mplain\x1b[32mgreen\x1b[0m")
	if text != "redplaingreen" {
		t.Fatalf("text = %q", text)
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(spans), spans)
	}
	if text[spans[0].Start:spans[0].End] != "red" {
		t.Errorf("first span covers %q, want %q", text[spans[0].Start:spans[0].End], "red")
	}
	if text[spans[1].Start:spans[1].End] != "green" {
		t.Errorf("second span covers %q, want %q", text[spans[1].Start:spans[1].End], "green")
	}
}

// A chunk boundary inside an escape must not leak bytes, and an open colour still applies (the pump's 4 KB reads).
func TestParser_SplitSequenceAcrossWrites(t *testing.T) {
	const full = "a\x1b[31mred\x1b[0mb"
	for cut := 1; cut < len(full); cut++ {
		t.Run("cut_"+strconv.Itoa(cut), func(t *testing.T) {
			p := NewParser()
			text1, spans1 := p.Write(full[:cut])
			text2, spans2 := p.Write(full[cut:])
			tailText, tailSpans := p.Flush()

			text := text1 + text2 + tailText
			spans := append(append(append([]Span{}, spans1...), spans2...), tailSpans...)

			if text != "aredb" {
				t.Fatalf("cut at %d: text = %q, want %q", cut, text, "aredb")
			}
			covered := 0
			for _, s := range spans {
				for off := s.Start; off < s.End; off++ {
					if off >= 1 && off < 4 {
						covered++
					}
					if off == 0 || off == 4 {
						t.Errorf("cut at %d: offset %d styled, want unstyled", cut, off)
					}
				}
			}
			if covered != 3 {
				t.Errorf("cut at %d: %d of 3 styled bytes covered, spans=%+v", cut, covered, spans)
			}
		})
	}
}

// A colour set in one chunk styles the next chunk's text, hence one Parser per stream.
func TestParser_OpenStyleCarriesAcrossWrites(t *testing.T) {
	p := NewParser()
	t1, s1 := p.Write("\x1b[31mfirst")
	t2, s2 := p.Write("second\x1b[0m")

	if t1 != "first" || t2 != "second" {
		t.Fatalf("text = %q + %q, want %q + %q", t1, t2, "first", "second")
	}
	if len(s1) != 1 || s1[0].FG != 1 {
		t.Fatalf("first write spans = %+v, want one red span", s1)
	}
	if len(s2) != 1 || s2[0].FG != 1 {
		t.Fatalf("second write spans = %+v, want the colour to carry", s2)
	}
	// Offsets are absolute, so the second write's span starts where the first left off.
	if s2[0].Start != len(t1) {
		t.Errorf("second span starts at %d, want %d (absolute offsets)", s2[0].Start, len(t1))
	}
}

// Offset read before a Write equals the absolute Start of that Write's first span; the runtime reports it as the
// chunk's base.
func TestParser_OffsetIsTheBaseOfTheNextWrite(t *testing.T) {
	p := NewParser()
	if got := p.Offset(); got != 0 {
		t.Fatalf("fresh parser Offset = %d, want 0", got)
	}
	// A surrogate pair, so a byte counter would disagree.
	lead, _ := p.Write("ok\U0001F600")
	wantAfterLead := 4 // "ok" + 2 units for the emoji
	if got := p.Offset(); got != wantAfterLead {
		t.Fatalf("Offset after %q = %d, want %d", lead, got, wantAfterLead)
	}
	base := p.Offset()
	_, spans := p.Write("\x1b[31mred\x1b[0m")
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Start != base {
		t.Errorf("span starts at %d, want the pre-write Offset %d", spans[0].Start, base)
	}
	// The flush path reports the same way.
	_, _ = p.Write("tail\x1b[3")
	flushBase := p.Offset()
	tail, _ := p.Flush()
	if flushBase+utf16Len(tail) != p.Offset() {
		t.Errorf("Offset advanced by %d over a %d-unit flush",
			p.Offset()-flushBase, utf16Len(tail))
	}
}

// An escape that never terminates must not swallow output forever.
func TestParser_UnterminatedSequenceIsReleasedAsText(t *testing.T) {
	p := NewParser()
	long := "\x1b[" + strings.Repeat("1;", maxPendingBytes)
	text, _ := p.Write(long)
	if text == "" {
		t.Error("an over-long unterminated sequence held every byte back, want it released as text")
	}
	if strings.ContainsRune(text, 0x1b) {
		t.Errorf("released text carries a raw ESC: %q", text[:min(len(text), 32)])
	}
}

func TestParser_FlushReleasesHeldBytes(t *testing.T) {
	p := NewParser()
	text, _ := p.Write("ok\x1b[3")
	if text != "ok" {
		t.Fatalf("text = %q, want %q (the partial sequence is held)", text, "ok")
	}
	tail, _ := p.Flush()
	if tail != "\ufffd[3" {
		t.Errorf("Flush = %q, want the held bytes back with ESC neutralized", tail)
	}
}

// A rune split across chunks reassembles: every split of the input must produce the one-shot text. Scoped to valid
// UTF-8 well inside maxPendingBytes.
func TestParser_MultiByteRuneSplitAcrossWrites(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "two byte rune leads", in: "\u00e9ok", want: "\u00e9ok"},
		{name: "two byte rune trails", in: "caf\u00e9", want: "caf\u00e9"},
		{name: "three byte rune leads", in: "\u2502ok", want: "\u2502ok"},
		{name: "four byte rune leads", in: "\U0001F600!", want: "\U0001F600!"},
		{name: "mixed widths", in: "a\u00e9\u2502\U0001F600z", want: "a\u00e9\u2502\U0001F600z"},
		{name: "escape between text and a wide rune", in: "x\x1b[1m\U0001F600", want: "x\U0001F600"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for cut := 0; cut <= len(tc.in); cut++ {
				t.Run("cut_"+strconv.Itoa(cut), func(t *testing.T) {
					p := NewParser()
					head, _ := p.Write(tc.in[:cut])
					rest, _ := p.Write(tc.in[cut:])
					tail, _ := p.Flush()
					if got := head + rest + tail; got != tc.want {
						t.Errorf("Write(%q)+Write(%q)+Flush() = %q, want %q",
							tc.in[:cut], tc.in[cut:], got, tc.want)
					}
					if got, want := p.Offset(), utf16Len(tc.want); got != want {
						t.Errorf("Offset() = %d, want %d units for %q", got, want, tc.want)
					}
				})
			}
		})
	}
}

// A style that covers no character produces no span; the client would have to special-case an empty range.
func TestParse_StyleWithNoTextProducesNoSpan(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantSpans int
	}{
		{name: "colour opened and nothing printed", in: "\x1b[31m", wantSpans: 0},
		{name: "colour opened and immediately reset", in: "\x1b[31m\x1b[0m", wantSpans: 0},
		{name: "colour opened after the text", in: "ok\x1b[31m", wantSpans: 0},
		{name: "text only after the second colour", in: "\x1b[31m\x1b[32mx", wantSpans: 1},
		{name: "colour changed twice mid text", in: "\x1b[31ma\x1b[32m\x1b[33mb", wantSpans: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse(tc.in)
			if len(spans) != tc.wantSpans {
				t.Errorf("Parse(%q) returned %d spans, want %d: %+v",
					tc.in, len(spans), tc.wantSpans, spans)
			}
			for i, s := range spans {
				if s.Start >= s.End {
					t.Errorf("Parse(%q) span %d = [%d,%d), want a non-empty range",
						tc.in, i, s.Start, s.End)
				}
			}
		})
	}
}

// At the bound an unterminated run is held for the next chunk; one byte past, it is released as text.
func TestParser_UnterminatedSequenceIsHeldUpToTheBound(t *testing.T) {
	atBound := "\x1b[" + strings.Repeat("1", maxPendingBytes-2)
	overBound := atBound + "1"

	t.Run("at the bound the run is held", func(t *testing.T) {
		p := NewParser()
		text, _ := p.Write(atBound)
		if text != "" {
			t.Errorf("Write of a %d-byte unterminated sequence emitted %d bytes of text, want it held",
				len(atBound), len(text))
		}
		tail, _ := p.Flush()
		if tail == "" {
			t.Error("Flush returned no text, want the held bytes back")
		}
		if strings.ContainsRune(tail, 0x1b) {
			t.Errorf("Flush text carries a raw ESC: %q", tail[:min(len(tail), 32)])
		}
	})

	t.Run("past the bound the run is released", func(t *testing.T) {
		p := NewParser()
		text, _ := p.Write(overBound)
		if text == "" {
			t.Errorf("Write of a %d-byte unterminated sequence held every byte, want it released as text",
				len(overBound))
		}
		if strings.ContainsRune(text, 0x1b) {
			t.Errorf("released text carries a raw ESC: %q", text[:min(len(text), 32)])
		}
		tail, _ := p.Flush()
		if tail != "" {
			t.Errorf("Flush = %q, want nothing left held", tail)
		}
	})
}

func TestRGB_RoundTripsAndIsDistinctFromPaletteIndices(t *testing.T) {
	c := RGB(10, 20, 30)
	if c < rgbFlag {
		t.Errorf("RGB(...) = %d, want it above rgbFlag (%d)", c, rgbFlag)
	}
	// The encodings share one int32, so no palette index may land in the truecolour range.
	for i := range 256 {
		if idx := int32(i); idx >= rgbFlag {
			t.Fatalf("palette index %d collides with the RGB range", idx)
		}
	}
	if got := (c >> 16) & 0xff; got != 10 {
		t.Errorf("red component = %d, want 10", got)
	}
	if got := (c >> 8) & 0xff; got != 20 {
		t.Errorf("green component = %d, want 20", got)
	}
	if got := c & 0xff; got != 30 {
		t.Errorf("blue component = %d, want 30", got)
	}
}

// Agent command output is untrusted: the agent picks the command and the command the bytes.
func FuzzParse(f *testing.F) {
	f.Add("plain text")
	f.Add("\x1b[31mred\x1b[0m")
	f.Add("\x1b[38;2;1;2;3mtrue\x1b[0m")
	f.Add("\x1b[38;5;208m256\x1b[0m")
	f.Add("\x1b[")
	f.Add("\x1b")
	f.Add("\x1b]8;;http://example.com\x07link\x1b]8;;\x07")
	f.Add("\x1b[999999999999999999999m")
	f.Add("\x1b[1;;;;;;mx")
	f.Add("a\x1b[2Kb\x1b[3Jc")
	f.Add("\x1b[7minverse\x1b[27m")
	f.Add("\x1bPq\x1b\\")
	f.Add("\x1b[4:3munderline\x1b[m")
	f.Add("\x1b[>4;2mx")
	f.Add("a\x1b%Gb")
	// The fuzzer's own finds, each a real defect. A long string-terminated sequence, where the pending bound meets
	// chunked agreement:
	f.Add("\x1bX" + strings.Repeat("0", 63) + "\x07")
	// An escape between a rune's lead and continuation bytes, which pushed UTF-16 offsets past the end:
	f.Add("\xe6\x1b[1m\xbd\xbd")
	// A run of invalid bytes, which strings.ToValidUTF8 collapses into one replacement:
	f.Add("\xbd\xbd\xbd")
	// Bytes that pass a lead-byte mask but never lead a valid rune (0xC0/0xC1 overlong, 0xF5-0xF7 past U+10FFFF), so a
	// tail-hold waiting for a continuation waits forever. Seeded because the corpus reached none of them.
	f.Add("\xc0")
	f.Add("\xc0\x80")
	f.Add("\xc1\xbf")
	f.Add("\xf5\x80\x80\x80")
	f.Add("a\xf6b")
	f.Add("\x1b[1m\xf7\x80")

	f.Fuzz(func(t *testing.T, in string) {
		text, spans := Parse(in)

		// 1. The text never grows in UTF-16 units: each input byte yields at most one unit (U+FFFD is three bytes but one
		// unit).
		if utf16Len(text) > len(in) {
			t.Fatalf("text grew: %d UTF-16 units out of %d input bytes", utf16Len(text), len(in))
		}

		// 2. Spans address real, ordered, non-overlapping ranges, checked in UTF-16 space.
		limit := utf16Len(text)
		prevEnd := 0
		for i, s := range spans {
			if s.Start < 0 || s.End > limit || s.Start >= s.End {
				t.Fatalf("span %d = [%d,%d) is out of range for %d UTF-16 units of text", i, s.Start, s.End, limit)
			}
			if s.Start < prevEnd {
				t.Fatalf("span %d = [%d,%d) overlaps the previous span ending at %d", i, s.Start, s.End, prevEnd)
			}
			prevEnd = s.End
		}

		// 3. No span carries default styling.
		for i, s := range spans {
			if s.FG == ColorDefault && s.BG == ColorDefault && s.Attrs == 0 {
				t.Fatalf("span %d styles nothing: %+v", i, s)
			}
		}

		// 4. The text never carries an ESC: it is persisted and re-served, so this is what replaces sanitize.StripANSI.
		if strings.ContainsRune(text, 0x1b) {
			t.Fatalf("plain text still contains ESC: %q", text)
		}

		// 5. Streaming agrees with one-shot for inputs within maxPendingBytes; past it a bounded streaming parser cannot
		// (the fuzzer found a 4 KB `ESC X ... BEL`). 1-4 hold for every input.
		if len(in) > maxPendingBytes {
			return
		}
		for cut := 0; cut <= len(in); cut++ {
			p := NewParser()
			a, _ := p.Write(in[:cut])
			b, _ := p.Write(in[cut:])
			tail, _ := p.Flush()
			if a+b+tail != text {
				t.Fatalf("split at %d gave %q, one-shot gave %q", cut, a+b+tail, text)
			}
			// 6. Offset matches the emitted text at every split, or live spans rebase onto the wrong character.
			if got, want := p.Offset(), utf16Len(text); got != want {
				t.Fatalf("split at %d: Offset = %d, want %d units of emitted text", cut, got, want)
			}
			if cut > 64 {
				break // bound the loop; the interesting cuts are early
			}
		}
	})
}

// Span offsets are UTF-16 code units for JavaScript indexing; a byte offset would slice garbage past the first
// non-ASCII character.
func TestParse_OffsetsAreUTF16CodeUnits(t *testing.T) {
	cases := []struct {
		name string
		lead string // unstyled text before the styled word
		want int    // expected UTF-16 offset of the styled word
	}{
		{name: "ascii", lead: "abc", want: 3},
		// U+00E9: 2 bytes, 1 unit.
		{name: "latin1 supplement", lead: "caf\u00e9", want: 4},
		// U+2502: 3 bytes, 1 unit.
		{name: "box drawing", lead: "\u2502\u2502", want: 2},
		// U+FFFF, the last rune below the surrogate range: 3 bytes, 1 unit.
		{name: "last single unit rune", lead: "\uffff\uffff", want: 2},
		// U+1F600: 4 bytes, a surrogate pair of 2 units.
		{name: "emoji is a surrogate pair", lead: "\U0001F600", want: 2},
		{name: "mixed", lead: "a\u00e9\u2502\U0001F600", want: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, spans := Parse(tc.lead + "\x1b[31mred\x1b[0m")
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}
			if spans[0].Start != tc.want {
				t.Errorf("span starts at %d, want %d (UTF-16 units in %q)", spans[0].Start, tc.want, tc.lead)
			}
			if spans[0].End != tc.want+3 {
				t.Errorf("span ends at %d, want %d", spans[0].End, tc.want+3)
			}
			// Slicing by the span's UTF-16 offsets yields exactly the styled word.
			if got := utf16Slice(text, spans[0].Start, spans[0].End); got != "red" {
				t.Errorf("utf16 slice = %q, want %q", got, "red")
			}
		})
	}
}

// utf16Slice indexes s as a browser would, so a test asserts what the client paints.
func utf16Slice(s string, start, end int) string {
	units := 0
	var out []rune
	for _, r := range s {
		w := 1
		if r > 0xffff {
			w = 2
		}
		if units >= start && units+w <= end {
			out = append(out, r)
		}
		units += w
	}
	return string(out)
}

func TestUTF16Len(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{in: "", want: 0},
		{in: "abc", want: 3},
		{in: "caf\u00e9", want: 4},
		{in: "\u2502", want: 1},
		// U+FFFF is the last one-unit rune; U+10000 the first surrogate pair.
		{in: "\uffff", want: 1},
		{in: "\U00010000", want: 2},
		{in: "\U0001F600", want: 2},
		{in: "a\U0001F600b", want: 4},
	}
	for _, tc := range cases {
		if got := utf16Len(tc.in); got != tc.want {
			t.Errorf("utf16Len(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
