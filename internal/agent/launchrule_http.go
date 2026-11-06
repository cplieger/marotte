package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/trigger"
)

// eventRuleChecker runs an event rule's save-time forge reads; a refusal is a *launchrule.ForgeCheckError.
type eventRuleChecker interface {
	CheckEventRule(ctx context.Context, ev *launchrule.EventSpec) error
}

// launchRuleView is a stored rule with its row, and a clock rule's next run computed server-side so the summary
// matches what will happen. Status is absent, and Error set, for a rule whose state file would not load.
type launchRuleView struct {
	NextRunAt *time.Time         `json:"next_run_at,omitempty"`
	Status    *launchrule.Status `json:"status,omitempty"`
	// Set only for event rules.
	Cursor     *trigger.CursorSummary `json:"cursor,omitempty"`
	Error      string                 `json:"error,omitempty"`
	HeldEvents []trigger.HeldEvent    `json:"held_events,omitempty"`
	launchrule.Rule
	Held int `json:"held"`
}

func launchRuleViewOf(r *launchrule.Rule, st *launchrule.State, readErr error) launchRuleView {
	v := launchRuleView{Rule: *r}
	if readErr != nil {
		v.Error = readErr.Error()
		return v
	}
	status := st.Status
	v.Status, v.Held = &status, st.Held()
	if r.When.Event != nil {
		held, c, err := trigger.Summarize(st, time.Now())
		if err != nil {
			v.Error = err.Error()
			return v
		}
		v.Cursor, v.HeldEvents = &c, held
	}
	if r.Enabled && r.When.Clock != nil {
		// schedule.NextRunFrom as the runner uses it, floored at now.
		if next, err := schedule.NextRunFrom(*r.When.Clock, status.Anchor, time.Now()); err == nil {
			v.NextRunAt = &next
		}
	}
	return v
}

func (rr *runRoutes) registerLaunchRules(mux *http.ServeMux, write func(string, http.HandlerFunc)) {
	if rr.runs.launchRules == nil {
		return
	}
	mux.HandleFunc("GET /api/launch-rules", rr.handleLaunchRuleList)
	write("POST /api/launch-rules", rr.handleLaunchRuleCreate)
	write("PUT /api/launch-rules/{id}", rr.handleLaunchRuleReplace)
	write("DELETE /api/launch-rules/{id}", rr.handleLaunchRuleDelete)
}

func (rr *runRoutes) handleLaunchRuleList(w http.ResponseWriter, _ *http.Request) {
	st := rr.runs.launchRules
	rules := st.List()
	out := make([]launchRuleView, 0, len(rules))
	for i := range rules {
		state, err := st.State(rules[i].ID)
		if errors.Is(err, launchrule.ErrNotFound) {
			continue
		}
		out = append(out, launchRuleViewOf(&rules[i], &state, err))
	}
	httpserver.WriteJSON(w, map[string]any{"rules": out})
}

func (rr *runRoutes) handleLaunchRuleCreate(w http.ResponseWriter, r *http.Request) {
	rule, _, ok := readRuleBody(w, r, false)
	if !ok || !rr.prepareRule(r.Context(), w, &rule, nil) {
		return
	}
	saved, err := rr.runs.launchRules.Create(r.Context(), &rule)
	rr.answerSave(w, &saved, err)
}

// handleLaunchRuleReplace serves PUT /api/launch-rules/{id}: the rule's recipe stays, everything else is
// replaced. A body may carry confirm_drop, the held-event count an edit of the watched population drops.
func (rr *runRoutes) handleLaunchRuleReplace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	prev, found := rr.runs.launchRules.Get(id)
	if !found {
		httpreply.NotFound(w, "no such launch rule")
		return
	}
	rule, confirmDrop, ok := readRuleBody(w, r, true)
	if !ok {
		return
	}
	if rule.Source == "" {
		rule.Source = prev.Source
	}
	if rule.Source != prev.Source {
		httpreply.BadRequest(w, "source cannot change: add a rule for the other workflow and delete this one")
		return
	}
	if !rr.prepareRule(r.Context(), w, &rule, &prev) {
		return
	}
	saved, err := rr.runs.launchRules.Replace(r.Context(), id, &rule, confirmDrop)
	rr.answerSave(w, &saved, err)
}

func (rr *runRoutes) handleLaunchRuleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := rr.runs.launchRules.Delete(r.Context(), id)
	switch {
	case errors.Is(err, launchrule.ErrNotFound):
		httpreply.NotFound(w, "no such launch rule")
		return
	case err != nil:
		httpreply.ServerError(w, "could not delete the launch rule", err)
		return
	}
	slog.Info("launch rule deleted", "id", logsafe.Field(id))
	httpserver.WriteJSON(w, map[string]any{"ok": true})
}

// readRuleBody decodes a client's rule, refusing server-written and unknown fields with a 400. withConfirm takes
// confirm_drop out of the body first; it is the request's, not the rule's.
func readRuleBody(w http.ResponseWriter, r *http.Request, withConfirm bool) (launchrule.Rule, int, bool) {
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		httpreply.BadRequest(w, "could not read the launch rule")
		return launchrule.Rule{}, 0, false
	}
	confirmDrop := 0
	if withConfirm {
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) == nil {
			if raw, ok := fields["confirm_drop"]; ok {
				if json.Unmarshal(raw, &confirmDrop) != nil || confirmDrop < 0 {
					httpreply.BadRequest(w, "confirm_drop must be a count of held events")
					return launchrule.Rule{}, 0, false
				}
				delete(fields, "confirm_drop")
				data, _ = json.Marshal(fields)
			}
		}
	}
	rule, err := launchrule.DecodeRule(data)
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return launchrule.Rule{}, 0, false
	}
	return rule, confirmDrop, true
}

// prepareRule validates rule for a save and fills it from its recipe, answering the refusal itself. The source is
// checked against a live recipe listing, as at launch, so no arbitrary file can be scheduled, and so is the model
// pick. An event rule's forge is read unless the save leaves prev's when as it was.
func (rr *runRoutes) prepareRule(ctx context.Context, w http.ResponseWriter, rule, prev *launchrule.Rule) bool {
	if err := rule.Normalize(); err != nil {
		httpreply.BadRequest(w, err.Error())
		return false
	}
	if prev != nil {
		if err := launchrule.CheckEdit(prev, rule); err != nil {
			httpreply.BadRequest(w, err.Error())
			return false
		}
	}
	recipe, err := rr.runs.recipeBySource(ctx, rule.Source)
	switch {
	case err == nil:
	case writeEngineSlow(w, err):
		return false
	case errors.Is(err, errUnknownRecipe) || errors.Is(err, errMissingRecipeSource):
		httpreply.BadRequest(w, "unknown recipe: "+rule.Source)
		return false
	default:
		slog.Warn("launch rule save: recipe list failed", "error", logsafe.Field(err.Error()), "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("recipe list unavailable"))
		return false
	}
	for _, name := range slices.Sorted(maps.Keys(rule.Inputs)) {
		if _, declared := recipe.Inputs[name]; !declared {
			httpreply.BadRequest(w, "inputs."+name+" is not an input of "+recipe.Name)
			return false
		}
	}
	defaults := marotte.RunDefaults{Model: rule.Model, Effort: rule.Effort}
	if err := rr.runs.checkRunDefaults(ctx, defaults); writeDefaultsErr(w, err) {
		return false
	}
	rule.Source, rule.Name, rule.Model, rule.Effort = recipe.Source, recipe.Name, defaults.Model, defaults.Effort
	if rule.When.Event == nil || (prev != nil && reflect.DeepEqual(prev.When, rule.When)) {
		return true
	}
	return rr.checkForge(ctx, w, rule.When.Event)
}

func (rr *runRoutes) checkForge(ctx context.Context, w http.ResponseWriter, ev *launchrule.EventSpec) bool {
	if rr.runs.eventRules == nil {
		httpreply.BadRequest(w, "this server has no forge connections to watch")
		return false
	}
	err := rr.runs.eventRules.CheckEventRule(ctx, ev)
	if err == nil {
		return true
	}
	fe, ok := errors.AsType[*launchrule.ForgeCheckError](err)
	switch {
	case !ok:
		httpreply.ServerError(w, "could not check the forge", err)
	case fe.Unavailable:
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(fe.Text))
	default:
		httpreply.BadRequest(w, fe.Text)
	}
	return false
}

func (rr *runRoutes) answerSave(w http.ResponseWriter, saved *launchrule.Rule, err error) {
	if err != nil {
		writeSaveErr(w, err)
		return
	}
	state, stateErr := rr.runs.launchRules.State(saved.ID)
	slog.Info("launch rule saved", "id", saved.ID, "recipe", logsafe.Field(saved.Name), "enabled", saved.Enabled)
	httpserver.WriteJSON(w, launchRuleViewOf(saved, &state, stateErr))
}

func writeSaveErr(w http.ResponseWriter, err error) {
	if ve, ok := errors.AsType[*launchrule.ValidationError](err); ok {
		httpreply.BadRequest(w, ve.Error())
		return
	}
	if de, ok := errors.AsType[*launchrule.DropError](err); ok {
		httpreply.Conflict(w, de.Error())
		return
	}
	if errors.Is(err, launchrule.ErrNotFound) {
		httpreply.NotFound(w, "no such launch rule")
		return
	}
	httpreply.ServerError(w, "could not save the launch rule", err)
}
