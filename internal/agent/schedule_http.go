package agent

// Schedule REST behind the workflow rows' Schedule button: an upsert and a delete, since the client rewrites a schedule wholesale.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/schedule"
)

// scheduleView is a stored entry plus its resolved next run, computed server-side so the summary matches what will happen.
type scheduleView struct {
	NextRunAt  *time.Time      `json:"next_run_at,omitempty"`
	LastRunAt  *time.Time      `json:"last_run_at,omitempty"`
	WaitingFor *time.Time      `json:"waiting_for,omitempty"`
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	Name       string          `json:"name,omitempty"`
	LastStatus string          `json:"last_status,omitempty"`
	LastReason string          `json:"last_reason,omitempty"`
	Model      string          `json:"model,omitempty"`
	Effort     string          `json:"effort,omitempty"`
	Spec       launchrule.Spec `json:"spec"`
	Enabled    bool            `json:"enabled"`
}

func (rr *runRoutes) registerSchedule(mux *http.ServeMux, write func(string, http.HandlerFunc)) {
	if rr.runs.launchRules == nil {
		return
	}
	mux.HandleFunc("GET /api/schedules", rr.handleScheduleList)
	write("POST /api/schedules", rr.handleSchedulePut)
	write("DELETE /api/schedules/{id}", rr.handleScheduleDelete)
}

// handleScheduleList: GET /api/schedules → every schedule with its next run.
func (rr *runRoutes) handleScheduleList(w http.ResponseWriter, _ *http.Request) {
	clocks := rr.runs.launchRules.Clocks()
	out := make([]scheduleView, 0, len(clocks))
	for i := range clocks {
		out = append(out, scheduleViewOf(&clocks[i]))
	}
	httpserver.WriteJSON(w, map[string]any{"schedules": out})
}

// An uncomputable spec yields no next run, and an unreadable row its read error, so the row still renders.
func scheduleViewOf(e *launchrule.Clock) scheduleView {
	v := scheduleView{
		Spec: *e.When.Clock, ID: e.ID, Source: e.Source, Name: e.Name,
		LastStatus: string(e.LastStatus), LastReason: e.LastReason, Enabled: e.Enabled,
		Model: e.Model, Effort: e.Effort,
	}
	if e.Err != nil {
		v.LastStatus, v.LastReason = string(launchrule.StatusFailed), e.Err.Error()
		return v
	}
	if !e.LastRunAt.IsZero() {
		t := e.LastRunAt
		v.LastRunAt = &t
	}
	if !e.WaitingFor.IsZero() {
		t := e.WaitingFor
		v.WaitingFor = &t
	}
	if e.Enabled {
		// schedule.NextRunFrom as the runner uses it, floored at now.
		if next, err := schedule.NextRunFrom(*e.When.Clock, e.Anchor, time.Now()); err == nil {
			v.NextRunAt = &next
		}
	}
	return v
}

// handleSchedulePut serves POST /api/schedules: creates the source's clock rule, or replaces the one it has. The
// source is validated against a live recipe listing, as in handleLaunch, so no arbitrary file can be scheduled, and
// the model pick as at launch.
func (rr *runRoutes) handleSchedulePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source  string          `json:"source"`
		Model   string          `json:"model"`
		Effort  string          `json:"effort"`
		Spec    launchrule.Spec `json:"spec"`
		Enabled bool            `json:"enabled"`
	}
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpreply.BadRequest(w, "invalid schedule payload")
		return
	}
	recipe, err := rr.runs.recipeBySource(r.Context(), req.Source)
	switch {
	case err == nil:
	case writeEngineSlow(w, err):
		return
	case errors.Is(err, errUnknownRecipe) || errors.Is(err, errMissingRecipeSource):
		httpreply.BadRequest(w, "unknown recipe: "+req.Source)
		return
	default:
		slog.Warn("schedule save: recipe list failed", "error", logsafe.Field(err.Error()), "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("recipe list unavailable"))
		return
	}
	defaults := marotte.RunDefaults{Model: req.Model, Effort: req.Effort}
	if err := rr.runs.checkRunDefaults(r.Context(), defaults); writeDefaultsErr(w, err) {
		return
	}
	rule := launchrule.Rule{
		Source: recipe.Source, Name: recipe.Name,
		Model: defaults.Model, Effort: defaults.Effort,
		When: launchrule.When{Clock: &req.Spec}, Enabled: req.Enabled,
	}
	saved, saveErr := rr.saveClockRule(r.Context(), &rule)
	if ve, ok := errors.AsType[*launchrule.ValidationError](saveErr); ok {
		httpreply.BadRequest(w, ve.Error())
		return
	}
	if saveErr != nil {
		httpreply.ServerError(w, "could not save schedule", saveErr)
		return
	}
	slog.Info("schedule saved", "id", saved.ID, "recipe", logsafe.Field(recipe.Name),
		"freq", logsafe.Field(string(saved.When.Clock.Freq)), "enabled", saved.Enabled)
	httpserver.WriteJSON(w, scheduleViewOf(&saved))
}

// saveClockRule replaces the clock rule of rule's source, or creates one when the source has none.
func (rr *runRoutes) saveClockRule(ctx context.Context, rule *launchrule.Rule) (launchrule.Clock, error) {
	st := rr.runs.launchRules
	id := ""
	clocks := st.Clocks()
	for i := range clocks {
		if clocks[i].Source == rule.Source {
			id = clocks[i].ID
			break
		}
	}
	var saved launchrule.Rule
	var err error
	if id == "" {
		saved, err = st.Create(ctx, rule)
	} else {
		saved, err = st.Replace(ctx, id, rule)
	}
	if err != nil {
		return launchrule.Clock{}, err
	}
	c, ok := st.Clock(saved.ID)
	if !ok {
		return launchrule.Clock{}, launchrule.ErrNotFound
	}
	return c, nil
}

// handleScheduleDelete: DELETE /api/schedules/{id}.
func (rr *runRoutes) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "schedule id is required")
		return
	}
	if err := rr.runs.launchRules.Delete(r.Context(), id); err != nil && !errors.Is(err, launchrule.ErrNotFound) {
		httpreply.ServerError(w, "could not delete schedule", err)
		return
	}
	slog.Info("schedule deleted", "id", logsafe.Field(id))
	httpserver.WriteJSON(w, map[string]any{"ok": true})
}
