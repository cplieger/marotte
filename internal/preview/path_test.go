package preview

import (
	"errors"
	"testing"
)

func TestCheckPageShape(t *testing.T) {
	refused := []string{
		"rel/x.html", "/a/../b.html", "/a//b.html", "/a/./b.html", "/a/b.html/",
		"/a/b\x00.html", "/a/b.html.bak", "/a/b.svg", "/a/b",
	}
	for _, p := range refused {
		if err := checkPageShape(p); err == nil {
			t.Errorf("checkPageShape(%q) = nil, want a refusal", p)
		}
	}
	accepted := []string{
		"/a/b.HTML", "/a/b.htm", "/a/my page.html", "/a/a#b?.html", "/a/100%.html",
		"/a/x+y&z=1,;.html", "/a/café-日本.html",
	}
	for _, p := range accepted {
		if err := checkPageShape(p); err != nil {
			t.Errorf("checkPageShape(%q) = %v, want nil", p, err)
		}
	}
}

func TestCheckRelComponents(t *testing.T) {
	for _, rel := range []string{".git/x.html", "d/.hidden.html", `d/back\slash.html`, `d\x/i.html`} {
		if err := checkRelComponents(rel); err == nil {
			t.Errorf("checkRelComponents(%q) = nil, want a refusal", rel)
		}
	}
	if err := checkRelComponents("demo/sub/index.html"); err != nil {
		t.Errorf("checkRelComponents(demo/sub/index.html) = %v, want nil", err)
	}
}

func TestRelBeneath(t *testing.T) {
	cases := []struct {
		root, p, rel string
		ok           bool
	}{
		{"/workspace", "/workspace/demo/index.html", "demo/index.html", true},
		{"/workspace", "/workspace", "", false},
		{"/workspace", "/workspace-old/x.html", "", false},
		{"/workspace", "/etc/x.html", "", false},
		{"/", "/demo/index.html", "demo/index.html", true},
		{"/", "/", "", false},
	}
	for _, tc := range cases {
		if rel, ok := relBeneath(tc.root, tc.p); rel != tc.rel || ok != tc.ok {
			t.Errorf("relBeneath(%q, %q) = %q %v, want %q %v", tc.root, tc.p, rel, ok, tc.rel, tc.ok)
		}
	}
}

func TestSplitServePath(t *testing.T) {
	tok, rel, err := splitServePath("/preview/T/sub/a%252fb.html")
	if err != nil || tok != "T" || len(rel) != 2 || rel[0] != "sub" || rel[1] != "a%2fb.html" {
		t.Errorf("splitServePath(escaped name) = %q %q %v, want T [sub a%%2fb.html] nil", tok, rel, err)
	}
	_, rel, err = splitServePath("/preview/T/")
	if err != nil || len(rel) != 1 || rel[0] != "" {
		t.Errorf("splitServePath(trailing slash) = %q %v, want one empty trailing segment", rel, err)
	}
	for _, p := range []string{
		"/preview/T/..", "/preview/T/.", "/preview/T/%2e%2e", "/preview/T/a%2fb",
		"/preview/T/a%2Fb", "/preview/T/%5c", "/preview/T/%5C", `/preview/T/a\b`, "/preview/T/%00",
		"/preview/T//x", "/preview/T/%zz", "/other/T/x",
	} {
		if _, _, err := splitServePath(p); !errors.Is(err, errNonCanonical) {
			t.Errorf("splitServePath(%q) = %v, want errNonCanonical", p, err)
		}
	}
}

func TestContentTypeFor(t *testing.T) {
	cases := map[string]string{
		"a.HTML": "text/html; charset=utf-8", "a.mjs": "text/javascript; charset=utf-8",
		"a.css": "text/css; charset=utf-8", "a.PNG": "image/png", "a.json": "application/json",
		"a.woff2": "font/woff2", "a.unknown": "application/octet-stream", "noext": "application/octet-stream",
	}
	for name, want := range cases {
		if got := contentTypeFor(name); got != want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", name, got, want)
		}
	}
}
