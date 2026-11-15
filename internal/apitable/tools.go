package apitable

import (
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/marotte/internal/server"
	"github.com/cplieger/toolbelt/v3/httpapi"
)

// toolSummaries are the toolbelt routes' summaries, keyed by method and suffix.
var toolSummaries = map[string]string{
	"GET ":                   "The installed tools",
	"GET /search":            "Search the tool catalog and the Debian packages",
	"GET /jobs":              "The active tool job and the recent ones",
	"GET /catalog":           "Where the tool catalog came from and how fresh it is",
	"POST /catalog/refresh":  "Refresh the tool catalog",
	"POST ":                  "Add a tool",
	"POST /update":           "Update tools",
	"PATCH /{name}":          "Change a tool",
	"POST /{name}/install":   "Install a tool",
	"DELETE /{name}":         "Remove a tool",
	"POST /jobs/{id}/cancel": "Cancel a tool job",
}

// toolsEntries are toolbelt's routes under /api/tools, each typed and coded by toolbelt itself.
func toolsEntries() []route.Entry {
	rs := httpapi.Routes()
	out := make([]route.Entry, 0, len(rs))
	for i := range rs {
		rt := &rs[i]
		mt := server.ToolsMethodTypes(rt)
		codes := make(route.CodeSet, 0, len(rt.Codes))
		for _, c := range rt.Codes {
			codes = append(codes, route.Code{Code: c.Code, Status: c.Status})
		}
		out = append(out, route.Entry{
			Name:     "tools" + rt.Method[:1] + strings.ToLower(rt.Method[1:]) + toolName(rt.Suffix),
			Method:   rt.Method,
			Path:     "/api/tools" + rt.Suffix,
			Kind:     route.KindJSON,
			Params:   mt.Params,
			Request:  mt.Request,
			Query:    mt.Query,
			Response: mt.Response,
			Status:   mt.Status,
			Summary:  toolSummaries[rt.Method+" "+rt.Suffix],
			Access: route.Access{
				Class: route.ClassKeyScope, KeyScope: route.ScopeTools, Transport: route.TransportAny, Safe: rt.Method == http.MethodGet,
			},
			Owners: []route.CodeSet{codes, server.Codes},
		})
	}
	return out
}

// toolName is a toolbelt suffix as a name part: /jobs/{id}/cancel is JobsByIdCancel.
func toolName(suffix string) string {
	var b strings.Builder
	for seg := range strings.SplitSeq(suffix, "/") {
		if seg == "" {
			continue
		}
		if name, ok := strings.CutPrefix(seg, "{"); ok {
			b.WriteString("By")
			seg = strings.TrimSuffix(name, "}")
		}
		b.WriteString(strings.ToUpper(seg[:1]) + seg[1:])
	}
	return b.String()
}

// anyName is a catch-all entry's name: the method, then the mount (App for /, Subtree for a trailing slash).
func anyName(method, path string) string {
	var name strings.Builder
	name.WriteString(strings.ToLower(method))
	switch path {
	case "/":
		return name.String() + "App"
	case "/api":
		return name.String() + "ApiRoot"
	}
	for seg := range strings.SplitSeq(path, "/") {
		if seg != "" {
			name.WriteString(strings.ToUpper(seg[:1]) + seg[1:])
		}
	}
	if strings.HasSuffix(path, "/") {
		name.WriteString("Subtree")
	}
	return name.String()
}
