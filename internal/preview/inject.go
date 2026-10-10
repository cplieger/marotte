package preview

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

const shimMarker = "data-marotte-shim"

// Reading localStorage in a sandboxed frame without allow-same-origin throws a SecurityError, which
// breaks most demo pages on their first line.
const storageShim = `<script ` + shimMarker + `>(()=>{for(const n of["localStorage","sessionStorage"]){try{void window[n]}catch{const m=new Map,s={get length(){return m.size},key(i){return[...m.keys()][i]??null},getItem(k){k=String(k);return m.has(k)?m.get(k):null},setItem(k,v){m.set(String(k),String(v))},removeItem(k){m.delete(String(k))},clear(){m.clear()}};Object.defineProperty(window,n,{value:s,configurable:true})}}})();</script>`

func injectShim(body []byte) []byte {
	off := insertionOffset(body)
	out := make([]byte, 0, len(body)+len(storageShim))
	out = append(out, body[:off]...)
	out = append(out, storageShim...)
	return append(out, body[off:]...)
}

// insertionOffset picks where the shim goes: right after a <head> start tag met
// before any other element, else after an <html> start tag, else after the
// doctype, else at 0. It is never before a doctype anywhere in the document,
// because a script ahead of one puts the page into quirks mode. A tokenizer
// rather than a byte search, so a "<head>" inside a comment, a script string or
// an attribute value cannot match.
func insertionOffset(body []byte) int {
	z := html.NewTokenizer(bytes.NewReader(body))
	pos, chosen, decided := 0, 0, false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return chosen
		}
		pos += len(z.Raw())
		switch {
		case tt == html.DoctypeToken:
			chosen = pos
		case decided:
		case tt == html.CommentToken:
		case tt == html.TextToken:
			if strings.TrimLeft(string(z.Text()), " \t\r\n\f\ufeff") != "" {
				decided = true
			}
		case tt == html.StartTagToken || tt == html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "html":
				chosen = pos
			case "head":
				chosen, decided = pos, true
			default:
				decided = true
			}
		}
	}
}
