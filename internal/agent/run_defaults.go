package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
)

// errDefaultsRefused is a pick the served catalog does not offer: KAS accepts any id at launch
// and fails the step at its first model call, so the refusal happens before a save or a launch.
var errDefaultsRefused = errors.New("model pick refused")

// errDefaultsUncheckable means no model list could be read, so a pick cannot be checked.
var errDefaultsUncheckable = errors.New("the model list could not be read, so the model pick cannot be checked; try again")

// checkRunDefaults refuses a pick GET /api/config-template would not offer. The zero value is
// always taken.
func (rs *Runs) checkRunDefaults(ctx context.Context, d marotte.RunDefaults) error {
	if d.Model == "" {
		if d.Effort != "" {
			return fmt.Errorf("%w: an effort needs a model", errDefaultsRefused)
		}
		return nil
	}
	models, ok := rs.servedModels(ctx)
	if !ok {
		return errDefaultsUncheckable
	}
	if why := pickRefusal(models, d.Model, d.Effort); why != "" {
		return fmt.Errorf("%w: %s", errDefaultsRefused, why)
	}
	return nil
}

// pickRefusal is why models do not offer model run at effort ("" for the model's own default), or "" when
// they do. The run defaults and every step pin go through it, so they cannot disagree.
func pickRefusal(models []marotte.SessionModel, model, effort string) string {
	tiers := tiersIn(models, model)
	switch {
	case tiers.known == tiersUnknown:
		return fmt.Sprintf("the model %q is not offered to this account", model)
	case effort == "":
		return ""
	case tiers.known == tiersNone:
		return fmt.Sprintf("the model %q offers no effort levels", model)
	}
	// The pill's rule for a tier the model does not run, so the two cannot disagree.
	if landed := landMove(&effortInput{to: model, target: tiers}, effort); landed.unoffered != "" {
		return fmt.Sprintf("the model %q does not offer the effort level %q", model, effort)
	}
	return ""
}

// planNode is one node of listRecipes' plan (KAS's buildNodePlan): a step's declared pins, with any auto
// model written "auto", and a container's children under steps (sequence, repeat) or branches (parallel).
type planNode struct {
	NodeID      string     `json:"nodeId"`
	Type        string     `json:"type"`
	AgentName   string     `json:"agentName"`
	ModelID     string     `json:"modelId"`
	EffortLevel string     `json:"effortLevel"`
	Steps       []planNode `json:"steps"`
	Branches    []planNode `json:"branches"`
}

// workflowCreatorAgent is KAS's wf-workflow-creator, the one step agent the workflow-level model and
// effort never reach (isWorkflowCreatorAgent).
const workflowCreatorAgent = "wf-workflow-creator"

// resolvedPick is the model and effort KAS's resolver gives the step from what marotte can see: the step's
// own pin, else the run's default; "" where it inherits the recipe's root or the parent session.
func (n *planNode) resolvedPick(d marotte.RunDefaults) (model, effort string) {
	reaches := n.AgentName != workflowCreatorAgent
	model, effort = declaredModel(n.ModelID), n.EffortLevel
	if model == "" && reaches {
		model = declaredModel(d.Model)
	}
	if effort == "" && reaches {
		effort = d.Effort
	}
	return model, effort
}

// declaredModel is KAS's normalizeDeclaredModelId: an auto model declares nothing, so the step takes the
// next level's.
func declaredModel(id string) string {
	if id == marotte.ModelAuto || strings.HasPrefix(id, marotte.ModelAuto+"-") {
		return ""
	}
	return id
}

// pinnedSteps appends every step under nodes that pins a model or an effort.
func pinnedSteps(dst []*planNode, nodes []planNode) []*planNode {
	for i := range nodes {
		n := &nodes[i]
		if n.Type == "step" && (n.ModelID != "" || n.EffortLevel != "") {
			dst = append(dst, n)
		}
		dst = pinnedSteps(pinnedSteps(dst, n.Steps), n.Branches)
	}
	return dst
}

// checkRecipePins refuses a launch whose plan pins a step to a pick the served catalog does not offer, as
// checkRunDefaults does for the run's own pick. KAS only warns: an unserved model fails the step at its
// first model call and an unoffered effort runs at the model's default. A step whose model marotte cannot
// resolve has its effort checked against every served model's tiers. A plan with no pins reads no catalog.
func (rs *Runs) checkRecipePins(ctx context.Context, plan json.RawMessage, defaults marotte.RunDefaults) error {
	if len(plan) == 0 {
		return nil
	}
	var nodes []planNode
	if err := json.Unmarshal(plan, &nodes); err != nil {
		slog.Warn("recipe plan did not decode, so its step model picks are not checked", "error", err)
		return nil
	}
	pinned := pinnedSteps(nil, nodes)
	if len(pinned) == 0 {
		return nil
	}
	models, ok := rs.servedModels(ctx)
	if !ok {
		return errDefaultsUncheckable
	}
	for _, n := range pinned {
		model, effort := n.resolvedPick(defaults)
		var why string
		switch {
		case model != "":
			why = pickRefusal(models, model, effort)
		case effort != "" && !offeredByAny(models, effort):
			why = fmt.Sprintf("no model offers the effort level %q", effort)
		}
		if why != "" {
			return fmt.Errorf("%w: step %q: %s", errDefaultsRefused, n.NodeID, why)
		}
	}
	return nil
}

func offeredByAny(models []marotte.SessionModel, effort string) bool {
	return slices.ContainsFunc(models, func(m marotte.SessionModel) bool { return slices.Contains(m.EffortTiers, effort) })
}

// writeDefaultsErr answers a checkRunDefaults or checkRecipePins error, reporting whether err was one.
func writeDefaultsErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errDefaultsRefused):
		httpreply.BadRequest(w, err.Error())
	case errors.Is(err, errDefaultsUncheckable):
		writeTryLater(w, err.Error(), modelListUnreadableCode)
	default:
		return false
	}
	return true
}
