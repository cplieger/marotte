package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route/routetest"
)

const routeTag = "amxAEqwvwjG23476CxNmK6"

// viewPush records what the view and notice routes handed the push service.
type viewPush struct {
	pushStub
	views    []marotte.PageView
	tags     []string
	shown    []string
	declined []string
	mu       sync.Mutex
}

func (p *viewPush) View(tag string, v marotte.PageView) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tags = append(p.tags, tag)
	p.views = append(p.views, v)
}

func (p *viewPush) Shown(_, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shown = append(p.shown, id)
}

func (p *viewPush) Declined(_, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.declined = append(p.declined, id)
}

func (*viewPush) Raise(_ context.Context, _ marotte.ChatID, _ *marotte.NotificationPayload, _ uint64) func(context.Context) {
	return func(context.Context) {}
}

func newRouteHub(t *testing.T) (*Runtime, *viewPush) {
	t.Helper()
	cs := newTestChatStore()
	fp := &viewPush{}
	h := newRuntimeOn(t.Context(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs, WithPush(fp))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, fp
}

func postEvent(rt *Runtime, path, tag, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	rt.RegisterRoutes(routetest.On(mux))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if tag != "" {
		req.Header.Set(clientTagHeader, tag)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleView_RefusesABadReport(t *testing.T) {
	for name, tc := range map[string]struct{ tag, body string }{
		"no tag":           {"", `{"page":"p1","visible":true,"focused":true}`},
		"bad page id":      {routeTag, `{"page":"p 1","visible":true,"focused":true}`},
		"an editor tab":    {routeTag, `{"page":"p1","visible":true,"focused":true,"tab":{"kind":"editor","ref":"a.go"}}`},
		"a settings tab":   {routeTag, `{"page":"p1","visible":true,"focused":true,"tab":{"kind":"settings","ref":""}}`},
		"a bad chat ref":   {routeTag, `{"page":"p1","visible":true,"focused":true,"tab":{"kind":"chat","ref":"../x"}}`},
		"an empty run ref": {routeTag, `{"page":"p1","visible":true,"focused":true,"tab":{"kind":"run","ref":""}}`},
		"a 257-byte run":   {routeTag, `{"page":"p1","visible":true,"focused":true,"tab":{"kind":"run","ref":"` + strings.Repeat("w", 257) + `"}}`},
		"an unknown field": {routeTag, `{"page":"p1","visible":true,"focused":true,"path":"/etc"}`},
		"not json":         {routeTag, `{"page":`},
		"an oversize body": {routeTag, `{"page":"p1","visible":true,"focused":true,"pad":"` + strings.Repeat("x", 1100) + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			h, fp := newRouteHub(t)
			if rec := postEvent(h, "/api/events/view", tc.tag, tc.body); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			fp.mu.Lock()
			defer fp.mu.Unlock()
			if len(fp.views) != 0 {
				t.Errorf("views = %+v, want none from a refused report", fp.views)
			}
		})
	}
}

func TestHandleView_RecordsAGoodReport(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want marotte.PageView
	}{
		"a chat tab": {
			`{"page":"p1","visible":true,"focused":true,"tab":{"kind":"chat","ref":"c-1"}}`,
			marotte.PageView{Page: "p1", Visible: true, Focused: true, Tab: &marotte.TabRef{Kind: marotte.TabKindChat, Ref: "c-1"}},
		},
		"a run tab": {
			`{"page":"p1","visible":false,"focused":false,"tab":{"kind":"run","ref":"wf_1"}}`,
			marotte.PageView{Page: "p1", Tab: &marotte.TabRef{Kind: marotte.TabKindRun, Ref: "wf_1"}},
		},
		"a null tab": {
			`{"page":"p1","visible":true,"focused":false,"tab":null}`,
			marotte.PageView{Page: "p1", Visible: true},
		},
		"no tab field": {
			`{"page":"p1","visible":true,"focused":true}`,
			marotte.PageView{Page: "p1", Visible: true, Focused: true},
		},
		"a close": {
			`{"page":"p1","closed":true}`,
			marotte.PageView{Page: "p1", Closed: true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, fp := newRouteHub(t)
			if rec := postEvent(h, "/api/events/view", routeTag, tc.body); rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204; body %q", rec.Code, rec.Body.String())
			}
			fp.mu.Lock()
			defer fp.mu.Unlock()
			if len(fp.views) != 1 || fp.tags[0] != routeTag {
				t.Fatalf("views = %+v under %v, want one under the presented tag", fp.views, fp.tags)
			}
			got := fp.views[0]
			if got.Page != tc.want.Page || got.Visible != tc.want.Visible || got.Focused != tc.want.Focused ||
				got.Closed != tc.want.Closed || (got.Tab == nil) != (tc.want.Tab == nil) ||
				(got.Tab != nil && *got.Tab != *tc.want.Tab) {
				t.Errorf("view = %+v (tab %+v), want %+v (tab %+v)", got, got.Tab, tc.want, tc.want.Tab)
			}
		})
	}
}

func TestHandleNotice_RecordsWhatThePageDid(t *testing.T) {
	h, fp := newRouteHub(t)
	for _, body := range []string{`{"id":"n1","outcome":"shown"}`, `{"id":"n2","outcome":"declined"}`} {
		if rec := postEvent(h, "/api/events/notice", routeTag, body); rec.Code != http.StatusNoContent {
			t.Fatalf("POST %s = %d, want 204", body, rec.Code)
		}
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.shown) != 1 || fp.shown[0] != "n1" || len(fp.declined) != 1 || fp.declined[0] != "n2" {
		t.Errorf("shown %v, declined %v, want [n1] and [n2]", fp.shown, fp.declined)
	}
}

func TestHandleNotice_RefusesABadReport(t *testing.T) {
	for name, tc := range map[string]struct{ tag, body string }{
		"no tag":          {"", `{"id":"n1","outcome":"shown"}`},
		"bad id":          {routeTag, `{"id":"n 1","outcome":"shown"}`},
		"unknown outcome": {routeTag, `{"id":"n1","outcome":"maybe"}`},
		"oversize":        {routeTag, `{"id":"n1","outcome":"shown","pad":"` + strings.Repeat("x", 1100) + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			h, fp := newRouteHub(t)
			if rec := postEvent(h, "/api/events/notice", tc.tag, tc.body); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			fp.mu.Lock()
			defer fp.mu.Unlock()
			if len(fp.shown)+len(fp.declined) != 0 {
				t.Errorf("reports recorded = %v %v, want none", fp.shown, fp.declined)
			}
		})
	}
}
