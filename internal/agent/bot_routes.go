package agent

import (
	"net/http"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

const (
	msgNoBot     = "This chat is not a Bot."
	msgMemoryOff = "This Bot's memory is off."
	msgNoMemory  = "Couldn't read this Bot's memory"
)

func (rt *Runtime) registerBotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/bots/rule-catalog", handleRuleCatalog)
	mux.HandleFunc("GET /api/bots/{id}", rt.handleBotSetup)
	mux.HandleFunc("GET /api/bots/{id}/memory", rt.handleBotMemory)
	mux.HandleFunc("POST /api/bots/{id}/rules/test", rt.handleRuleTest)
}

// handleBotSetup serves GET /api/bots/{id}: the Bot's setup from its record, with its memory file's size and last
// write while memory is on, and the rule engine's in-memory facts on it. Never cached: it is a configuration read.
func (rt *Runtime) handleBotSetup(w http.ResponseWriter, r *http.Request) {
	c, ok := rt.botRecord(w, r)
	if !ok {
		return
	}
	out := marotte.BotSetup{
		Bot: c.Bot, Name: c.Name, Model: c.NextModel(), Effort: c.NextEffort(), Supervised: c.SupervisedMode,
		Engine: rt.botEngine(marotte.ChatID(c.ID)),
	}
	if c.Bot.Memory {
		read, _, err := bot.ReadMemory(c, botPaths())
		if err != nil {
			httpreply.ServerError(w, msgNoMemory, err)
			return
		}
		out.Memory = &marotte.BotMemoryStat{Bytes: read.Bytes, UpdatedAt: unixMilliOrZero(&read)}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpserver.WriteJSON(w, out)
}

// botEngine is the rule engine's in-memory facts on id; zero before the engine is wired.
func (rt *Runtime) botEngine(id marotte.ChatID) marotte.BotEngine {
	if rt.rules == nil {
		return marotte.BotEngine{}
	}
	st := rt.rules.Status(id)
	var out marotte.BotEngine
	if !st.WriteFailingSince.IsZero() {
		out.State, out.Since = marotte.BotEngineWriteFailing, st.WriteFailingSince.UnixMilli()
	}
	if !st.CeilingUntil.IsZero() {
		out.CeilingUntil = st.CeilingUntil.UnixMilli()
	}
	return out
}

// handleBotMemory serves GET /api/bots/{id}/memory: the Bot's notes, empty before its file exists, 404 while memory is
// off whatever the file holds.
func (rt *Runtime) handleBotMemory(w http.ResponseWriter, r *http.Request) {
	c, ok := rt.botRecord(w, r)
	if !ok {
		return
	}
	if !c.Bot.Memory {
		httpreply.NotFound(w, msgMemoryOff)
		return
	}
	read, _, err := bot.ReadMemory(c, botPaths())
	if err != nil {
		httpreply.ServerError(w, msgNoMemory, err)
		return
	}
	httpserver.WriteJSON(w, marotte.BotMemoryText{Text: read.Text, Bytes: read.Bytes, UpdatedAt: unixMilliOrZero(&read)})
}

// botRecord reads the {id} path value's record, answering 400 for an id that is no chat id and 404 for a chat with
// no bot block; false means the reply is written.
func (rt *Runtime) botRecord(w http.ResponseWriter, r *http.Request) (*marotte.Chat, bool) {
	id := r.PathValue("id")
	if !ids.ValidChatID(id) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return nil, false
	}
	c, ok := rt.chatStore.Get(r.Context(), marotte.ChatID(id))
	if !ok || c.Bot == nil {
		httpreply.NotFound(w, msgNoBot)
		return nil, false
	}
	return c, true
}

// unixMilliOrZero is the file's last write in unix ms, 0 before the file exists.
func unixMilliOrZero(read *bot.MemoryRead) int64 {
	if read.UpdatedAt.IsZero() {
		return 0
	}
	return read.UpdatedAt.UnixMilli()
}
