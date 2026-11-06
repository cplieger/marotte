package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
)

// Pattern is the route's ServeMux pattern; the method is checked in the handler, as on every marotte route.
const Pattern = routePrefix + "{session}" + routeSuffix

// What a delivery became: the chat's live turn read it as a steer, or it opened a turn as a prompt.
const (
	DeliveredSteer  = "steer"
	DeliveredPrompt = "prompt"
)

var (
	// ErrUnknownSession is a session id no chat holds; the route answers 404.
	ErrUnknownSession = errors.New("no chat holds this session")
	// ErrUnavailable is a delivery the engine cannot take now (draining, not ready, saturated); the route answers 503
	// with Retry-After.
	ErrUnavailable = errors.New("the agent engine cannot take a message now")
)

// TextRefusedError is a text the chat refuses to carry; the route answers 400 naming text.
type TextRefusedError struct{ Reason string }

func (e *TextRefusedError) Error() string { return "text: " + e.Reason }

// Deliverer hands one message to the chat holding session, steering its live turn or prompting it when idle, with
// source as the caller label. It answers DeliveredSteer or DeliveredPrompt, or ErrUnknownSession, ErrUnavailable, a
// *TextRefusedError or another error.
type Deliverer interface {
	Deliver(ctx context.Context, session, text, source string) (string, error)
}

// The body's bounds. A notification is a short wake-up, so text is capped well under a prompt's 512 KiB; the body
// cap leaves room for JSON escapes, which can take six bytes per text byte.
const (
	maxTextBytes      = 64 << 10
	maxSourceBytes    = 32
	maxDedupeKeyBytes = 128
	maxBodyBytes      = 512 << 10
)

// retryAfterSeconds is the Retry-After a 503 carries: long enough for an engine restart's first ready or a turn's
// steer buffer to drain, short enough that a wake-up stays timely.
const retryAfterSeconds = "5"

// Handler serves POST Pattern.
type Handler struct {
	deliv  Deliverer
	dedupe *dedupeTable
	now    func() time.Time
	gate   Gate
}

// NewHandler builds the route over gate, its credential, and deliv, the chats it delivers into.
func NewHandler(gate Gate, deliv Deliverer) *Handler {
	return &Handler{gate: gate, deliv: deliv, dedupe: newDedupeTable(), now: time.Now}
}

type requestBody struct {
	Text      *string `json:"text"`
	Source    *string `json:"source"`
	DedupeKey *string `json:"dedupe_key"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	if !h.gate.Authorize(w, r) {
		return
	}
	session := r.PathValue("session")
	if !ids.ValidSessionID(session) {
		httpserver.WriteJSONStatus(w, http.StatusNotFound, httpreply.ErrorJSON(ErrUnknownSession.Error()))
		return
	}
	httpserver.LimitBody(w, r, maxBodyBytes)
	var body requestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			httpserver.WriteJSONStatus(w, http.StatusRequestEntityTooLarge, httpreply.ErrorJSON("body: over 512 KiB"))
			return
		}
		httpreply.BadRequest(w, "body: want a JSON object with string fields text, source and dedupe_key")
		return
	}
	text, source, key, msg := validBody(&body)
	if msg != "" {
		httpreply.BadRequest(w, msg)
		return
	}
	h.deliver(r.Context(), w, dedupeKey{session: session, key: key}, text, source)
}

func (h *Handler) deliver(ctx context.Context, w http.ResponseWriter, k dedupeKey, text, source string) {
	switch h.dedupe.reserve(k, h.now()) {
	case reserveDuplicate:
		httpserver.WriteJSON(w, map[string]bool{"duplicate": true})
		return
	case reserveInFlight:
		unavailable(w, "a delivery with this dedupe_key is still in progress")
		return
	case reserveFresh:
	}
	verb, err := h.deliv.Deliver(ctx, k.session, text, source)
	if err != nil {
		h.dedupe.release(k)
		writeDeliveryError(w, k.session, source, err)
		return
	}
	h.dedupe.commit(k, h.now())
	slog.Info("notify delivered", "session_id", k.session, "source", source, "delivered", verb)
	httpserver.WriteJSON(w, map[string]string{"delivered": verb})
}

func writeDeliveryError(w http.ResponseWriter, session, source string, err error) {
	if refused, ok := errors.AsType[*TextRefusedError](err); ok {
		httpreply.BadRequest(w, refused.Error())
		return
	}
	switch {
	case errors.Is(err, ErrUnknownSession):
		httpserver.WriteJSONStatus(w, http.StatusNotFound, httpreply.ErrorJSON(ErrUnknownSession.Error()))
	case errors.Is(err, ErrUnavailable):
		unavailable(w, err.Error())
	default:
		slog.Warn("notify delivery failed", "session_id", session, "source", source, "error", err)
		httpserver.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON("the message could not be delivered"))
	}
}

func unavailable(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", retryAfterSeconds)
	httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(msg))
}

// validBody answers the body's trimmed text, source and dedupe key, or a 400 message naming the first bad field.
func validBody(b *requestBody) (text, source, key, msg string) {
	switch {
	case b.Text == nil || strings.TrimSpace(*b.Text) == "":
		return "", "", "", "text: required, and not only whitespace"
	case len(*b.Text) > maxTextBytes:
		return "", "", "", "text: over 64 KiB"
	case b.Source == nil || *b.Source == "":
		return "", "", "", "source: required"
	case len(*b.Source) > maxSourceBytes || !sourceSafe(*b.Source):
		return "", "", "", "source: want 1 to 32 characters of A-Z a-z 0-9 . _ -"
	case b.DedupeKey == nil || *b.DedupeKey == "":
		return "", "", "", "dedupe_key: required"
	case len(*b.DedupeKey) > maxDedupeKeyBytes || !printableASCII(*b.DedupeKey):
		return "", "", "", "dedupe_key: want 1 to 128 printable ASCII characters"
	}
	return strings.TrimSpace(*b.Text), *b.Source, *b.DedupeKey, ""
}

// sourceSafe keeps the caller label drawable on one line of the turn header with no escaping.
func sourceSafe(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func printableASCII(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
