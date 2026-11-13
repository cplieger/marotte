package agent

// The Memories tab: list, get, update and delete over KAS's _kiro/memory/* engine calls.
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

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// memoryStoreCap is KAS's record cap and largest list page, so one page holds the store.
const memoryStoreCap = 1000

// memoryMaxPages bounds the list walk should a short page ever carry a cursor.
const memoryMaxPages = 4

// memoryCallTimeout bounds one round trip, extended through an engine stall (engineOwner.stallAware); the first may
// start the bridge.
const memoryCallTimeout = 45 * time.Second

// memoryNotEnabledCode marks KAS's refusal for an account the memory experiment has not reached.
const memoryNotEnabledCode = "not_enabled"

// kasMemory is one record as KAS projects it; createdAt and updatedAt pass through as sent.
type kasMemory struct {
	Content    *string `json:"content"`
	ID         string  `json:"id"`
	MemoryType string  `json:"memoryType"`
	Title      string  `json:"title"`
	Summary    string  `json:"summary"`
	// Meta carries cloudState on a synced store: pending until the cloud took the record.
	Meta struct {
		Kiro struct {
			CloudState string `json:"cloudState"`
		} `json:"kiro"`
	} `json:"_meta"`
	CreatedAt json.RawMessage `json:"createdAt"`
	UpdatedAt json.RawMessage `json:"updatedAt"`
	Scopes    []string        `json:"scopes"`
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
	CloudState string          `json:"cloud_state,omitempty"`
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
		Content: m.Content, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, CloudState: m.Meta.Kiro.CloudState, Scopes: scopes,
	}
}

func (st *Settings) memoryCall(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	cctx, cancel := st.engine.stallAware(ctx, memoryCallTimeout)
	defer cancel()
	return st.engine.memoryRaw(cctx, method, params)
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

// writeMemoryErr answers a KAS refusal with KAS's text (409 coming_soon for import and export not yet served, 409
// not_enabled, else 400) and anything else, an answer marotte could not read included, with a generic 502.
func writeMemoryErr(w http.ResponseWriter, err error) {
	if _, ok := errors.AsType[*marotte.RPCError](err); ok && !marotte.ReplyUnread(err) {
		if m, typed := rpcerr.MappedOf(err); typed && m.ErrorType == memoryImportComingSoonClass {
			httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(
				"Memory import and export are not available for this account yet.", memoryImportComingSoonCode,
			))
			return
		}
		text := rpcerr.Text(err)
		if strings.Contains(text, "Memory is not enabled") {
			httpserver.WriteJSONStatus(w, http.StatusConflict,
				httpreply.ErrorJSONWithCode("Memory is not available for this account.", memoryNotEnabledCode))
			return
		}
		httpreply.BadRequest(w, text)
		return
	}
	slog.Warn("memory op failed", "error", err)
	httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("memory request failed"))
}

// memoryGonePrefix opens KAS's refusal to delete an id its store no longer holds, which kiro-cli's own TUI
// counts as deleted: another tab, the extraction subagent or the model's memory tool removed it first.
const memoryGonePrefix = `no memory with id "`

func memoryAlreadyGone(err error) bool {
	_, ok := errors.AsType[*marotte.RPCError](err)
	return ok && strings.HasPrefix(rpcerr.Text(err), memoryGonePrefix)
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
	httpserver.WriteJSON(w, memoryListResponse{Memories: rows, Cap: memoryStoreCap})
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
		st.memoryOne(r.Context(), methodKiroMemoryGet, map[string]any{"id": id}).write(w)
	case http.MethodPatch:
		var body memoryUpdateReq
		if !httpreply.DecodeJSON(w, r, &body) {
			return
		}
		params := map[string]any{"id": id}
		for key, v := range map[string]*string{
			"newTitle": body.Title, "summary": body.Summary, keyContent: body.Content, "memoryType": body.MemoryType,
		} {
			if v != nil {
				params[key] = *v
			}
		}
		if len(params) == 1 {
			httpreply.BadRequest(w, "nothing to update")
			return
		}
		st.memoryWrite(w, r, "memory update", func(ctx context.Context) engineReply {
			return st.memoryOne(ctx, methodKiroMemoryUpdate, params)
		})
	case http.MethodDelete:
		st.memoryWrite(w, r, "memory delete", func(ctx context.Context) engineReply {
			_, err := st.memoryCall(ctx, methodKiroMemoryDelete, map[string]any{"id": id})
			if err != nil && !memoryAlreadyGone(err) {
				return memoryFailure(err)
			}
			return engineReply{write: httpserver.OK, applied: true}
		})
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPatch, http.MethodDelete)
	}
}

// memoryOne is a get or update of one record, answered with the record KAS returns.
func (st *Settings) memoryOne(ctx context.Context, method string, params map[string]any) engineReply {
	raw, err := st.memoryCall(ctx, method, params)
	if err != nil {
		return memoryFailure(err)
	}
	row, err := decodeOneMemory(raw)
	if err != nil {
		// KAS answered, so an update landed; only its record is unreadable.
		return engineReply{err: err, write: func(w http.ResponseWriter) { writeMemoryErr(w, err) }, applied: true}
	}
	return engineReply{write: func(w http.ResponseWriter) { httpserver.WriteJSON(w, memoryOneResponse{Memory: row}) }, applied: true}
}

// memoryWrite runs a memory change as an engine mutation, so the list refetches on memories_changed once it lands.
func (st *Settings) memoryWrite(w http.ResponseWriter, r *http.Request, verb string, call func(context.Context) engineReply) {
	st.lifecycle.answerEngineMutation(w, r, engineMutation{verb: verb, changed: st.broadcastMemoriesChanged, call: call})
}

func (st *Settings) broadcastMemoriesChanged() {
	st.broadcast(context.Background(), marotte.NewEvent(marotte.EventMemoriesChanged, "", marotte.MemoriesChangedPayload{}))
}

func memoryFailure(err error) engineReply {
	return engineReply{err: err, write: func(w http.ResponseWriter) { writeMemoryErr(w, err) }}
}

// registerMemoryRoutes wires the Memories endpoints, method-less so a refused method answers 405 + Allow.
func (st *Settings) registerMemoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/memory", st.handleMemoryCollection)
	mux.HandleFunc("/api/memory/sync", st.handleMemorySync)
	mux.HandleFunc("/api/memory/import-prompt", st.handleMemoryImportPrompt)
	mux.HandleFunc("/api/memory/import", st.handleMemoryImport)
	mux.HandleFunc("/api/memory/export", st.handleMemoryExport)
	mux.HandleFunc("/api/memory/{id}", st.handleMemoryOne)
}
