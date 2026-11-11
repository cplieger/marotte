package forges

import (
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route"
)

// routeMux is what a route registration needs: the server's registrar, or a test's mux. An alias, so
// RegisterRoutes has the server's routeHandler signature.
type routeMux = interface{ Bind(route.Binding) }

// RepoPath is a repository route's {id} and {repo}.
type RepoPath struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
}

// RepoItemPath is a pull request's or an issue's {id}, {repo} and {number}.
type RepoItemPath struct {
	ID     string `json:"id"`
	Repo   string `json:"repo"`
	Number string `json:"number"`
}

// ListQuery is a list route's query: the state filter, the page cursor, and Refresh "1" to read
// past the cache.
type ListQuery struct {
	State   string `json:"state,omitempty"`
	After   string `json:"after,omitempty"`
	Refresh string `json:"refresh,omitempty"`
}

// RefreshQuery is a cached read's query: Refresh "1" reads past the cache.
type RefreshQuery struct {
	Refresh string `json:"refresh,omitempty"`
}

// ChecksQuery is GET .../checks' query: the branch or SHA to fold.
type ChecksQuery struct {
	Ref string `json:"ref"`
}

// RerunQuery is POST .../rerun's query: the head the caller's row displayed.
type RerunQuery struct {
	HeadSHA string `json:"head_sha,omitempty"`
}

// ForgeList is GET /api/forges' body: the connections, the kinds, and per public kind whether
// Marotte's own OAuth application signs it in.
type ForgeList struct {
	OAuth  map[string]bool   `json:"oauth"`
	Forges []ConfiguredForge `json:"forges"`
	Kinds  []Kind            `json:"kinds"`
}

// GrantRef is a device grant's poll or cancel body.
type GrantRef struct {
	GrantID string `json:"grant_id"`
}

const msgInvalidPR = "invalid PR number"

// Detection, a token sign-in and a device-grant start share one bucket: any
// caller can send each, and each sends requests to a forge.
const (
	outboundBurst    = 6
	outboundInterval = 10 * time.Second
)

// RegisterRoutes binds every /api/forges route, one binding per endpoint.
func (h *HTTPHandler) RegisterRoutes(m routeMux) {
	outbound := httpserver.RateLimiter(outboundBurst, outboundInterval,
		httpserver.WithRateLimitError("rate_limited", "Too many sign-ins or detections were started. Wait a moment and try again"))
	id := reflect.TypeFor[marotte.IDPath]()
	repo := reflect.TypeFor[RepoPath]()
	item := reflect.TypeFor[RepoItemPath]()
	list := reflect.TypeFor[ListQuery]()
	refresh := reflect.TypeFor[RefreshQuery]()
	bind := func(p string, f http.Handler, ms ...route.MethodTypes) { m.Bind(route.Methods(p, f, ms...)) }

	bind("/api/forges", http.HandlerFunc(h.handleForgesList), route.Typed[struct{}, ForgeList](http.MethodGet))
	bind("/api/forges/login", outbound(http.HandlerFunc(h.handleLogin)), route.Typed[TokenLogin, LoginResult](http.MethodPost))
	bind("/api/forges/detect", outbound(http.HandlerFunc(h.handleDetect)), route.Typed[DetectRequest, Detection](http.MethodPost))
	bind("/api/forges/inventory", http.HandlerFunc(h.handleInventory), route.Typed[struct{}, InventoryList](http.MethodGet))
	bind("/api/forges/inventory/refresh", http.HandlerFunc(h.handleInventoryRefresh),
		route.Typed[struct{}, InventoryRefresh](http.MethodPost).WithStatus(http.StatusAccepted))
	bind("/api/forges/inventory/watch", http.HandlerFunc(h.handleInventoryWatch),
		route.Typed[InventoryWatch, struct{}](http.MethodPost).WithStatus(http.StatusNoContent).
			WithHeaders(route.Header{Name: clientTagHeader, Arg: "sseClient", Required: true, MinBytes: 1, MaxBytes: 64}))
	name := reflect.TypeFor[marotte.NamePath]()
	bind("/api/forges/oauth/{name}/start", outbound(grantRoute(h.handleGrantStart)),
		route.Typed[GrantStart, DeviceFlowResponse](http.MethodPost).WithParams(name))
	bind("/api/forges/oauth/{name}/poll", grantRoute(func(w http.ResponseWriter, r *http.Request, _ Kind) { h.handleGrantPoll(w, r) }),
		route.Typed[GrantRef, PollResult](http.MethodPost).WithParams(name))
	bind("/api/forges/oauth/{name}/cancel", grantRoute(func(w http.ResponseWriter, r *http.Request, _ Kind) { h.handleGrantCancel(w, r) }),
		route.Typed[GrantRef, struct{}](http.MethodPost).WithParams(name).WithStatus(http.StatusNoContent))

	bind("/api/forges/{id}", http.HandlerFunc(h.handleForge),
		route.Typed[struct{}, ConfiguredForge](http.MethodGet).WithParams(id),
		route.Typed[struct{}, httpreply.OKBody](http.MethodDelete).WithParams(id))
	bind("/api/forges/{id}/probe", h.known(h.handleProbe), route.Typed[struct{}, ProbeResult](http.MethodPost).WithParams(id))
	bind("/api/forges/{id}/capabilities", h.known(h.handleCapabilities),
		route.Typed[struct{}, Capabilities](http.MethodGet).WithParams(id).WithQuery(refresh))
	bind("/api/forges/{id}/owners", h.known(h.handleOwners), route.Typed[OwnersWrite, OwnerScopes](http.MethodPut).WithParams(id))
	bind("/api/forges/{id}/repos", h.known(h.handleRepoList),
		route.Typed[struct{}, RepoList](http.MethodGet).WithParams(id).WithQuery(list))

	base := "/api/forges/{id}/repos/{repo}"
	bind(base+"/prs", h.repoRoute(h.handlePRCollection),
		route.Typed[struct{}, PRList](http.MethodGet).WithParams(repo).WithQuery(list),
		route.Typed[PRCreate, PR](http.MethodPost).WithParams(repo))
	bind(base+"/prs/{number}", h.itemRoute(msgInvalidPR, handlePRDetail), route.Typed[struct{}, PRDetail](http.MethodGet).WithParams(item))
	bind(base+"/prs/{number}/merge", h.itemRoute(msgInvalidPR, h.handleMerge),
		route.Typed[struct{}, MergeStatus](http.MethodGet).WithParams(item),
		route.Typed[PRMerge, MergeResult](http.MethodPost).WithParams(item))
	bind(base+"/prs/{number}/close", h.itemRoute(msgInvalidPR, h.prAction(stateClose)), route.Typed[struct{}, PRChanged](http.MethodPost).WithParams(item))
	bind(base+"/prs/{number}/reopen", h.itemRoute(msgInvalidPR, h.prAction("reopen")), route.Typed[struct{}, PRChanged](http.MethodPost).WithParams(item))
	bind(base+"/prs/{number}/rerun", h.itemRoute(msgInvalidPR, h.handlePRRerun),
		route.Typed[struct{}, httpreply.OKBody](http.MethodPost).WithParams(item).WithQuery(reflect.TypeFor[RerunQuery]()))
	bind(base+"/issues", h.repoRoute(h.handleIssueCollection),
		route.Typed[struct{}, IssueList](http.MethodGet).WithParams(repo).WithQuery(list),
		route.Typed[IssueCreate, Issue](http.MethodPost).WithParams(repo))
	bind(base+"/issues/{number}/close", h.itemRoute("invalid issue number", h.handleIssueClose), route.Typed[struct{}, Issue](http.MethodPost).WithParams(item))
	bind(base+"/checks", h.repoRoute(handleChecks),
		route.Typed[struct{}, CommitChecks](http.MethodGet).WithParams(repo).WithQuery(reflect.TypeFor[ChecksQuery]()))
	bind(base+"/releases", h.repoRoute(h.handleReleases),
		route.Typed[struct{}, ReleaseList](http.MethodGet).WithParams(repo).WithQuery(list),
		route.Typed[ReleaseCreate, Release](http.MethodPost).WithParams(repo))
	bind(base+"/labels", h.repoRoute(handleLabels),
		route.Typed[struct{}, LabelList](http.MethodGet).WithParams(repo).WithQuery(list))
	bind(base+"/affordances", h.repoRoute(h.handleAffordances),
		route.Typed[struct{}, RepoAffordances](http.MethodGet).WithParams(repo).WithQuery(refresh))
}

// handleForge is GET /api/forges/{id}, the row, and DELETE, the sign-out.
func (h *HTTPHandler) handleForge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Method == http.MethodDelete {
		h.handleDisconnect(w, r, id)
		return
	}
	row, ok := h.manager.view(id)
	if !ok {
		httpreply.NotFound(w, "unknown forge id")
		return
	}
	httpserver.WriteJSON(w, row)
}

// known serves fn for a connection id the manager holds a row for, and 404 for any other.
func (h *HTTPHandler) known(fn func(http.ResponseWriter, *http.Request, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := h.manager.view(id); !ok {
			httpreply.NotFound(w, "unknown forge id")
			return
		}
		fn(w, r, id)
	})
}

// repoRoute serves fn with the connection's read handle and its {repo} decoded once on the
// connection's family, the canonical encoding this server mints and keys every cache entry on.
func (h *HTTPHandler) repoRoute(fn func(http.ResponseWriter, *http.Request, *readHandle, forgeapi.RepoRef)) http.Handler {
	return h.known(func(w http.ResponseWriter, r *http.Request, id string) {
		held, ok := h.handle(w, id)
		if !ok {
			return
		}
		ref, err := forgeapi.DecodeRepoRef(r.PathValue("repo"), held.family)
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		fn(w, r, &held, ref)
	})
}

// itemRoute is repoRoute for a pull request or an issue, with {number} parsed and refused with
// invalid as its message; a non-positive number is the library's to refuse.
func (h *HTTPHandler) itemRoute(invalid string, fn func(http.ResponseWriter, *http.Request, *readHandle, forgeapi.RepoRef, int)) http.Handler {
	return h.repoRoute(func(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef) {
		number, err := strconv.Atoi(r.PathValue("number"))
		if err != nil {
			httpreply.BadRequest(w, invalid)
			return
		}
		fn(w, r, rh, ref, number)
	})
}

// grantRoute serves fn for a kind with a device grant, and 404 for any other.
func grantRoute(fn func(http.ResponseWriter, *http.Request, Kind)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := Kind(r.PathValue("name"))
		if _, ok := marotteApp(kind.family()); !ok {
			httpreply.NotFound(w, "no device grant for that forge")
			return
		}
		fn(w, r, kind)
	})
}
