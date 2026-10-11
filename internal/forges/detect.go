package forges

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/families"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// detectBudget bounds one detection, which asks up to three families in turn.
const detectBudget = 30 * time.Second

var errDetectWebBaseInvalid = errors.New("forges: web_base_url must be a scheme and host:port")

// detectBody is an address and the token the connect after it will use: no
// family constructor builds a client without a credential.
type detectBody struct {
	Token      string `json:"token"`
	WebBaseURL string `json:"web_base_url"`
	trustFields
}

// record is the connection b describes, refused unless its web base is an
// origin the connect route could then take.
func (b *detectBody) record() (connectionRecord, error) {
	u, err := url.Parse(b.WebBaseURL)
	if err != nil || !originOn(b.WebBaseURL, u.Host) {
		return connectionRecord{}, errDetectWebBaseInvalid
	}
	rec := b.recordOn(u.Host)
	rec.WebBaseURL = u.Scheme + "://" + u.Host
	return rec, nil
}

// handleDetect serves POST /api/forges/detect: the kind of forge at an address
// nobody has named a kind for. It stores nothing.
func (h *HTTPHandler) handleDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body detectBody
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "invalid json")
		return
	}
	rec, err := body.record()
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest, httpreply.ErrorJSONWithCode(err.Error(), codeWebBaseInvalid))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), detectBudget)
	defer cancel()
	family, err := h.manager.clients.detect(ctx, &rec, body.Token)
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, Detection{Kind: detectedKind(family, rec.Host)})
}

// No client is kept: families.Open closes the candidates it rejects and detect closes the one that
// answered.
func (f *clientFactory) detect(ctx context.Context, rec *connectionRecord, token string) (forgeapi.Family, error) {
	opts := f.options(rec)
	// With no source at all the constructors refuse anonymous_refused before
	// sending; an empty static token would be sent.
	if token != "" {
		opts = append(opts, forgeapi.WithCredentialSource(staticSource(token)))
	}
	core, family, err := families.Open(ctx, connectionFor(rec), opts...)
	if err != nil {
		return family, err
	}
	core.Close()
	return family, nil
}

func detectedKind(family forgeapi.Family, host string) Kind {
	switch family {
	case forgeapi.FamilyGitHub:
		return KindGitHub
	case forgeapi.FamilyGitLab:
		return KindGitLab
	case forgeapi.FamilyGitea:
		if host == KindCodeberg.defaultHost() {
			return KindCodeberg
		}
		return KindGitea
	}
	return ""
}
