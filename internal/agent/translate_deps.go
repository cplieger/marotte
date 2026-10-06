package agent

import (
	"reflect"

	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
)

// IsScheduled reports whether a schedule launched the run, from its lease, granted before the first lifecycle frame.
func (rs *Runs) IsScheduled(workflowID string) bool {
	l, ok := rs.lease(workflowID)
	return ok && l.Origin == runlease.OriginScheduled
}

// RunLabel is the label the launch sent KAS, rebuilt from the lease; run_start
// carries none of its own.
func (rs *Runs) RunLabel(workflowID string) string {
	l, ok := rs.lease(workflowID)
	if !ok {
		return ""
	}
	return launchOrigin{origin: l.Origin}.runLabel(l.Recipe)
}

// translateRoles is the translate wiring, named so it can be asserted. Every field is read at construction,
// so requireWired names a missing owner instead of a first-frame panic.
func (rt *Runtime) translateRoles() *translate.Roles {
	return requireWired(&translate.Roles{
		Bus:   rt.bus,
		Chats: rt.chatStore,
		// The coordinator: a chat frame folds into the open turn, opening one if needed.
		Turns: rt.coord,
		// The run surface: a step frame folds into the run record's open turn.
		Runs:  rt.runs,
		Lines: rt.lines,
		// The steers this server sent: the discriminator between the user's words and a workflow's in the same buffer.
		Steers: rt.steerLedger,
		// The projection of KAS's buffer the connect replay serves, a separate owner from Steers: an origin is TTL'd,
		// a waiting steer lives as long as KAS's buffer.
		SteerBuffer:  rt.bus,
		PendingPerms: rt.bus,
		// rt: BridgeRespond resolves the reply bridge by chat id.
		Respond:        rt,
		Push:           rt.coord,
		Sessions:       rt.coord,
		Terminals:      rt.agentTerms,
		HookStatus:     rt.hookStatus,
		Catalog:        rt.catalog,
		Slash:          rt.slash,
		SteeringIssues: rt.steeringIssues,
		WorkDir:        rt.lifecycle.workDir,
		MCP:            rt.mcpRegistry,
		Governance:     rt.config,
		RunOrigin:      rt.runs,
		RunBounds:      rt.runs,
		// The coordinator: ending a turn needs the chat's bridge and its prompt cancel.
		TurnInterrupt: rt.coord,
		Metering:      rt.coord,
		Bracket:       rt.coord,
	})
}

// requireWired panics unless every role has an owner: a nil role is a constructor mistake, else the server dies
// on its first frame. Reflection covers new fields automatically.
func requireWired(r *translate.Roles) *translate.Roles {
	v := reflect.ValueOf(r).Elem()
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			if f.String() == "" {
				panic("agent: translate role " + name + " is empty at construction")
			}
		case reflect.Interface:
			// A role from a nil *T is a non-nil interface; only Elem() catches it.
			if f.IsNil() {
				panic("agent: translate role " + name + " is nil at construction — its owner is " +
					"assigned after the roles literal in agent.New")
			}
			if e := f.Elem(); e.Kind() == reflect.Pointer && e.IsNil() {
				panic("agent: translate role " + name + " holds a nil " + e.Type().String() +
					" — its owner is assigned after the roles literal in agent.New")
			}
		default:
			panic("agent: translate role " + name + " has unchecked kind " + f.Kind().String())
		}
	}
	return r
}
