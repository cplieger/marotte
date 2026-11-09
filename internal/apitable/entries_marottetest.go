//go:build marotte_test

package apitable

import (
	"net/http"
	"reflect"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/marotte/internal/server"
)

// testHookEntries are the test hooks' endpoints, served by a marotte_test build only.
func testHookEntries() []route.Entry {
	return []route.Entry{
		{
			Name:     "postTestPreviewToken",
			Method:   http.MethodPost,
			Path:     "/api/test/preview-token",
			Kind:     route.KindJSON,
			Request:  reflect.TypeFor[server.PreviewTokenRequest](),
			Response: reflect.TypeFor[marotte.PreviewGrant](),
			Summary:  "Test builds: mint a preview grant with a chosen expiry",
			Access:   route.Access{Class: route.ClassSession, Transport: route.TransportAny},
		},
		{
			Name:    "postTestPreviewResolver",
			Method:  http.MethodPost,
			Path:    "/api/test/preview-resolver",
			Kind:    route.KindJSON,
			Request: reflect.TypeFor[server.PreviewResolverRequest](),
			Status:  http.StatusNoContent,
			Summary: "Test builds: set the preview resolver's answer",
			Access:  route.Access{Class: route.ClassSession, Transport: route.TransportAny},
		},
		{
			Name:    "postTestPreviewGrow",
			Method:  http.MethodPost,
			Path:    "/api/test/preview-grow",
			Kind:    route.KindJSON,
			Request: reflect.TypeFor[server.PreviewGrowRequest](),
			Status:  http.StatusNoContent,
			Summary: "Test builds: grow a file after its next size check",
			Access:  route.Access{Class: route.ClassSession, Transport: route.TransportAny},
		},
		{
			Name:     "getTestSse",
			Method:   http.MethodGet,
			Path:     "/api/test/sse",
			Kind:     route.KindJSON,
			Response: reflect.TypeFor[server.SSEProbe](),
			Summary:  "Test builds: the event stream's counters",
			Access:   route.Access{Class: route.ClassSession, Transport: route.TransportAny, Safe: true},
		},
		{
			Name:    "postTestSseCloseAfter",
			Method:  http.MethodPost,
			Path:    "/api/test/sse/close-after",
			Kind:    route.KindJSON,
			Request: reflect.TypeFor[server.SSECloseAfter](),
			Status:  http.StatusNoContent,
			Summary: "Test builds: close the next stream after N frames",
			Access:  route.Access{Class: route.ClassSession, Transport: route.TransportAny},
		},
	}
}
