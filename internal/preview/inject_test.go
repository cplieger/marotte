package preview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"golang.org/x/net/html"
)

func TestInsertionOffset(t *testing.T) {
	cases := []struct{ name, in, before string }{
		{"after head with attrs", `<!doctype html><html><head lang="x"><title>t</title>`, `<!doctype html><html><head lang="x">`},
		{"head after a leading comment", `<!-- c --><head><meta>`, `<!-- c --><head>`},
		{"uppercase head", `<!DOCTYPE html><HEAD><title>`, `<!DOCTYPE html><HEAD>`},
		{"html with no head", `<!doctype html><html lang="en"><body>x`, `<!doctype html><html lang="en">`},
		{"doctype only", `<!doctype html><p>x`, `<!doctype html>`},
		{"nothing", `<p>hello`, ``},
		{"head in a comment is not a head", `<!doctype html><!-- <head> --><p>x`, `<!doctype html>`},
		{"head in a script string is not a head", `<!doctype html><script>"<head>"</script><head>`, `<!doctype html>`},
		{"head in an attribute is not a head", `<!doctype html><div title="<head>"><head>`, `<!doctype html>`},
		{"body before head", `<!doctype html><html><body><head>`, `<!doctype html><html>`},
		{"BOM before doctype", "\ufeff<!doctype html><head>", "\ufeff<!doctype html><head>"},
		{"late doctype after a long comment", "<!--" + strings.Repeat("x", 70<<10) + "--><!doctype html><p>", "<!--" + strings.Repeat("x", 70<<10) + "--><!doctype html>"},
		{"head past 64 KiB", "<!doctype html><!--" + strings.Repeat("y", 70<<10) + "--><head>", "<!doctype html><!--" + strings.Repeat("y", 70<<10) + "--><head>"},
		{"text before a doctype", "junk<!doctype html><p>", "junk<!doctype html>"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			if got := insertionOffset([]byte(tc.in)); got != len(tc.before) {
				t.Errorf("insertionOffset(%.40q) = %d, want %d", tc.in, got, len(tc.before))
			}
		})
	}
}

func TestInjectShim_ExactlyOnce(t *testing.T) {
	out := injectShim([]byte(`<!doctype html><head></head><body><script>1</script></body>`))
	if n := bytes.Count(out, []byte(shimMarker)); n != 1 {
		t.Errorf("injectShim: %d shims, want 1", n)
	}
}

func FuzzInject(f *testing.F) {
	for _, s := range []string{"", "<!doctype html><head>", "<p>x<!doctype html>", "<!--x--><html><head>", "\ufeff<HEAD>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		off := insertionOffset([]byte(in))
		out := injectShim([]byte(in))
		if string(out[:off])+string(out[off+len(storageShim):]) != in {
			t.Fatalf("injectShim(%q) changed the document", in)
		}
		z := html.NewTokenizer(strings.NewReader(in))
		pos := 0
		for tt := z.Next(); tt != html.ErrorToken; tt = z.Next() {
			pos += len(z.Raw())
			if tt == html.DoctypeToken && pos > off {
				t.Fatalf("injectShim(%q) put the shim at %d, before a doctype ending at %d", in, off, pos)
			}
		}
	})
}

func TestExtractHint(t *testing.T) {
	type want struct {
		preset marotte.PreviewPreset
		width  int
		source marotte.PreviewHintSource
	}
	cases := []struct {
		name string
		in   string
		want *want
	}{
		{"phone", `<meta name="marotte-preview" content="phone">`, &want{preset: "phone", source: "meta"}},
		{"tablet any case", `<META NAME="Marotte-Preview" CONTENT=" TABLET ">`, &want{preset: "tablet", source: "meta"}},
		{"desktop", `<meta name="marotte-preview" content="desktop">`, &want{preset: "desktop", source: "meta"}},
		{"width", `<meta name="marotte-preview" content="1280">`, &want{width: 1280, source: "meta"}},
		{"clamped low", `<meta name="marotte-preview" content="100">`, &want{width: 200, source: "meta"}},
		{"clamped high", `<meta name="marotte-preview" content="9999">`, &want{width: 4096, source: "meta"}},
		{"garbage", `<meta name="marotte-preview" content="huge">`, nil},
		{"viewport width", `<meta name="viewport" content="initial-scale=1, width=1024">`, &want{width: 1024, source: "viewport"}},
		{"device-width", `<meta name="viewport" content="width=device-width">`, nil},
		{"absent", `<title>x</title>`, nil},
		{"marotte-preview wins", `<meta name="viewport" content="width=800"><meta name="marotte-preview" content="phone">`, &want{preset: "phone", source: "meta"}},
		{"after body ignored", `<body><meta name="marotte-preview" content="phone">`, nil},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			got := extractHint([]byte(tc.in))
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("extractHint(%q) = %+v, want nil", tc.in, *got)
			case tc.want != nil && got == nil:
				t.Errorf("extractHint(%q) = nil, want %+v", tc.in, *tc.want)
			case tc.want != nil && (got.Preset != tc.want.preset || got.Width != tc.want.width || got.Source != tc.want.source):
				t.Errorf("extractHint(%q) = %+v, want %+v", tc.in, *got, *tc.want)
			}
		})
	}
}
