package push

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route"
)

// routeMux is what a route registration needs: the server's registrar, or a test's mux. An alias, so
// RegisterRoutes has the server's routeHandler signature.
type routeMux = interface{ Bind(route.Binding) }

// RegisterRoutes binds /api/push/vapid-key, /api/push/subscribe and /api/push/unsubscribe.
func (s *Service) RegisterRoutes(m routeMux) {
	m.Bind(route.Methods("/api/push/vapid-key", http.HandlerFunc(s.handleVAPIDKey), route.Typed[struct{}, VAPIDKey](http.MethodGet)))
	m.Bind(route.Methods("/api/push/subscribe", http.HandlerFunc(s.handleSubscribe),
		route.Typed[marotte.PushSubscription, httpreply.OKBody](http.MethodPost)))
	m.Bind(route.Methods("/api/push/unsubscribe", http.HandlerFunc(s.handleUnsubscribe),
		route.Typed[Unsubscribe, httpreply.OKBody](http.MethodPost)))
}

// VAPIDKey is GET /api/push/vapid-key's body.
type VAPIDKey struct {
	PublicKey string `json:"publicKey"`
}

// Unsubscribe is POST /api/push/unsubscribe's body.
type Unsubscribe struct {
	Endpoint string `json:"endpoint"`
}

func (s *Service) handleVAPIDKey(w http.ResponseWriter, _ *http.Request) {
	httpserver.WriteJSON(w, VAPIDKey{PublicKey: s.publicKey()})
}

func (s *Service) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
	var sub marotte.PushSubscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil || sub.Endpoint == "" {
		httpreply.BadRequest(w, "invalid subscription")
		return
	}
	if !isAllowedPushEndpoint(sub.Endpoint) {
		httpreply.BadRequest(w, "push endpoint not from a recognised browser push service")
		return
	}
	// Validate key material at the ingress boundary so Send's hot
	// path doesn't have to defend against structurally bad keys.
	// RFC 8291: p256dh is the 65-byte uncompressed P-256 point
	// (0x04 || X(32) || Y(32)); auth is 16 random bytes.
	pub, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil || len(pub) != 65 || pub[0] != 0x04 {
		httpreply.BadRequest(w, "invalid p256dh key")
		return
	}
	auth, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil || len(auth) != 16 {
		httpreply.BadRequest(w, "invalid auth secret")
		return
	}
	s.Subscribe(sub)
	httpreply.OK(w)
}

func (s *Service) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
	var body Unsubscribe
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
		httpreply.BadRequest(w, "invalid endpoint")
		return
	}
	s.unsubscribe(body.Endpoint)
	httpreply.OK(w)
}
