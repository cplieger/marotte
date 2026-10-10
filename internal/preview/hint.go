package preview

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"golang.org/x/net/html"
)

// hintScanBytes bounds the viewport-hint scan; both metas belong in <head>.
const hintScanBytes = 64 << 10

const (
	minHintWidth = 200
	maxHintWidth = 4096
)

// An explicit <meta name="marotte-preview"> wins over a numeric <meta name="viewport"> width;
// width=device-width, no meta and an unparsable value all mean no hint.
func extractHint(body []byte) *marotte.PreviewHint {
	if len(body) > hintScanBytes {
		body = body[:hintScanBytes]
	}
	z := html.NewTokenizer(bytes.NewReader(body))
	var viewport *marotte.PreviewHint
	for {
		meta, more := nextHeadMeta(z)
		if !more {
			return viewport
		}
		if !meta {
			continue
		}
		preview, vp := metaHints(z)
		if preview != nil {
			return preview
		}
		if viewport == nil {
			viewport = vp
		}
	}
}

func nextHeadMeta(z *html.Tokenizer) (meta, more bool) {
	tt := z.Next()
	if tt == html.ErrorToken {
		return false, false
	}
	if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
		return false, true
	}
	tag, hasAttr := z.TagName()
	if string(tag) == "body" {
		return false, false
	}
	return string(tag) == "meta" && hasAttr, true
}

func metaHints(z *html.Tokenizer) (preview, viewport *marotte.PreviewHint) {
	name, content := metaAttrs(z)
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "marotte-preview":
		return previewMetaHint(content), nil
	case "viewport":
		return nil, viewportHint(content)
	}
	return nil, nil
}

func metaAttrs(z *html.Tokenizer) (name, content string) {
	for {
		k, v, more := z.TagAttr()
		switch string(k) {
		case "name":
			name = string(v)
		case "content":
			content = string(v)
		}
		if !more {
			return name, content
		}
	}
}

func previewMetaHint(content string) *marotte.PreviewHint {
	v := strings.ToLower(strings.TrimSpace(content))
	switch p := marotte.PreviewPreset(v); p {
	case marotte.PreviewPresetPhone, marotte.PreviewPresetTablet, marotte.PreviewPresetDesktop:
		return &marotte.PreviewHint{Preset: p, Source: marotte.PreviewHintMeta}
	}
	if w, ok := hintWidth(v); ok {
		return &marotte.PreviewHint{Width: w, Source: marotte.PreviewHintMeta}
	}
	return nil
}

func viewportHint(content string) *marotte.PreviewHint {
	for part := range strings.FieldsFuncSeq(content, func(r rune) bool { return r == ',' || r == ';' }) {
		k, v, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "width") {
			continue
		}
		if w, ok := hintWidth(strings.TrimSpace(v)); ok {
			return &marotte.PreviewHint{Width: w, Source: marotte.PreviewHintViewport}
		}
		return nil
	}
	return nil
}

func hintWidth(v string) (int, bool) {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return min(max(n, minHintWidth), maxHintWidth), true
}
