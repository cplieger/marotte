package agent

// The Memories tab: list, get, update and delete over KAS's _kiro/memory/* on the utility bridge.
// KAS owns the store. Calls carry no sessionId, so KAS answers from the account's entitlement
// alone, whatever the Memory setting.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/webhttp/v3"
)

// memoryStoreCap is KAS's record cap and largest list page, so one page holds the store.
const memoryStoreCap = 1000

// memoryMaxPages bounds the list walk should a short page ever carry a cursor.
const memoryMaxPages = 4

// memoryCallTimeout bounds one round trip; the first may start the bridge.
const memoryCallTimeout = 45 * time.Second

// memoryNotEnabledCode marks KAS's refusal for an account the memory experiment has not reached.
const memoryNotEnabledCode = "not_enabled"

// kasMemory is one record as KAS projects it; createdAt and updatedAt pass through as sent.
type kasMemory struct {
	ID         string          `json:"id"`
	MemoryType string          `json:"memoryType"`
	Title      string          `json:"title"`
	Summary    string          `json:"summary"`
	Content    *string         `json:"content"`
	CreatedAt  json.RawMessage `json:"createdAt"`
	UpdatedAt  json.RawMessage `json:"updatedAt"`
	Scopes     []string        `json:"scopes"`
}

// memoryRow is one record on the wire; Content is set only by GET /api/memory/{id}.
type memoryRow struct {
	ID         string          `json:"id"`
	MemoryType string          `json:"memory_type"`
	Title      string          `json:"title"`
	Summary    string          `json:"summary"`
	Content    *string         `json:"content,omitempty"`
	CreatedAt  json.RawMessage `json:"created_at,omitempty"`
	UpdatedAt  json.RawMessage `json:"updated_at,omitempty"`
	Scopes     []string        `json:"scopes"`
}

// Cap feeds the "N of 1,000" label.
type memoryListResponse struct {
	Memories []memoryRow `json:"memories"`
	Cap      int         `json:"cap"`
}

type memoryOneResponse struct {
	Memory memoryRow `json:"memory"`
}

// An absent field is left unchanged.
type memoryUpdateReq struct {
	Title      *string `json:"title"`
	Summary    *string `json:"summary"`
	Content    *string `json:"content"`
	MemoryType *string `json:"memory_type"`
}

func rowFromKAS(m *kasMemory) memoryRow {
	scopes := m.Scopes
	if len(scopes) == 0 {
		scopes = []string{"Global"}
	}
	return memoryRow{
		ID: m.ID, MemoryType: m.MemoryType, Title: m.Title, Summary: m.Summary,
		Content: m.Content, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, Scopes: scopes,
	}
}

func (st *Settings) memoryCall(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, memoryCallTimeout)
	defer cancel()
	return st.utility().session.memoryRaw(cctx, method, params)
}

// One page normally covers it.
func (st *Settings) memoryList(ctx context.Context) ([]memoryRow, error) {
	out := []memoryRow{}
	params := map[string]any{"limit": memoryStoreCap}
	for range memoryMaxPages {
		raw, err := st.memoryCall(ctx, methodKiroMemoryList, params)
		if err != nil {
			return nil, err
		}
		var page struct {
			NextCursor string      `json:"nextCursor"`
			Memories   []kasMemory `json:"memories"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		for i := range page.Memories {
			out = append(out, rowFromKAS(&page.Memories[i]))
		}
		if page.NextCursor == "" {
			return out, nil
		}
		params = map[string]any{"limit": memoryStoreCap, "cursor": page.NextCursor}
	}
	return out, nil
}

func decodeOneMemory(raw json.RawMessage) (memoryRow, error) {
	var res struct {
		Memory *kasMemory `json:"memory"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return memoryRow{}, err
	}
	if res.Memory == nil {
		return memoryRow{}, errors.New("memory: reply carried no record")
	}
	return rowFromKAS(res.Memory), nil
}

// writeMemoryErr answers a KAS refusal with KAS's text (409 not_enabled, else 400) and anything else with a generic 502.
func writeMemoryErr(w http.ResponseWriter, err error) {
	if _, ok := errors.AsType[*marotte.RPCError](err); ok {
		text := rpcerr.Text(err)
		if strings.Contains(text, "Memory is not enabled") {
			webhttp.WriteJSONStatus(w, http.StatusConflict,
				httpreply.ErrorJSONWithCode("Memory is not available for this account.", memoryNotEnabledCode))
			return
		}
		httpreply.BadRequest(w, text)
		return
	}
	slog.Warn("memory op failed", "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("memory request failed"))
}

func (st *Settings) handleMemoryCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	rows, err := st.memoryList(r.Context())
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	webhttp.WriteJSON(w, memoryListResponse{Memories: rows, Cap: memoryStoreCap})
}

// handleMemoryOne serves one record by id (a title can repeat): GET with content, PATCH edits, DELETE removes.
func (st *Settings) handleMemoryOne(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		httpreply.BadRequest(w, "id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		st.memoryReadOne(w, r, methodKiroMemoryGet, map[string]any{"id": id})
	case http.MethodPatch:
		var body memoryUpdateReq
		if !httpreply.DecodeJSON(w, r, &body) {
			return
		}
		params := map[string]any{"id": id}
		for key, v := range map[string]*string{
			"newTitle": body.Title, "summary": body.Summary, "content": body.Content, "memoryType": body.MemoryType,
		} {
			if v != nil {
				params[key] = *v
			}
		}
		if len(params) == 1 {
			httpreply.BadRequest(w, "nothing to update")
			return
		}
		st.memoryReadOne(w, r, methodKiroMemoryUpdate, params)
	case http.MethodDelete:
		if _, err := st.memoryCall(r.Context(), methodKiroMemoryDelete, map[string]any{"id": id}); err != nil {
			writeMemoryErr(w, err)
			return
		}
		webhttp.Ok(w)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPatch, http.MethodDelete)
	}
}

func (st *Settings) memoryReadOne(w http.ResponseWriter, r *http.Request, method string, params map[string]any) {
	raw, err := st.memoryCall(r.Context(), method, params)
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	row, err := decodeOneMemory(raw)
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	webhttp.WriteJSON(w, memoryOneResponse{Memory: row})
}

// registerMemoryRoutes wires the Memories endpoints, method-less so a refused method answers 405 + Allow.
func (st *Settings) registerMemoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/memory", st.handleMemoryCollection)
	mux.HandleFunc("/api/memory/{id}", st.handleMemoryOne)
}
