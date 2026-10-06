package agent

import "reflect"

// requireCollaborators panics unless every pointer, interface and func field of every collaborator the Runtime
// built is populated. Collaborators bind theirs by value at construction, so a nil there stays nil; that shipped as a
// nil-receiver panic three times. An optional field carries `wiring:"optional"`.
func requireCollaborators(h *Runtime) {
	// Keyed literals: a positional list would swap meaning if fieldalignment reordered them.
	for _, c := range []struct {
		v    any
		name string
	}{
		{name: "runs", v: h.runs},
		{name: "bus", v: h.bus},
		{name: "config", v: h.config},
		{name: "inbound", v: h.inbound},
		{name: "agentTerms", v: h.agentTerms},
		{name: "mcpRegistry", v: h.mcpRegistry},
		{name: "runRoutes", v: h.runRoutes},
		{name: "powers", v: h.powers},
		{name: "utility", v: h.utility},
		{name: "replay", v: h.replay},
		// Every collaborator constructed inside New with fields taken from h.
		{name: "coord", v: h.coord},
	} {
		requirePopulated(c.name, c.v)
	}
}

// requirePopulated panics on the first nil pointer, interface or func field of v.
func requirePopulated(owner string, v any) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.IsNil() {
		panic("agent: collaborator " + owner + " was never constructed")
	}
	s := rv.Elem()
	for i := range s.NumField() {
		f := s.Field(i)
		if s.Type().Field(i).Tag.Get("wiring") == "optional" {
			continue
		}
		switch f.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Func:
			if f.IsNil() {
				panic("agent: " + owner + "." + s.Type().Field(i).Name +
					" is nil after New — its collaborator is assigned later in the " +
					"constructor than the literal that binds it")
			}
		default:
			// Value fields have no nil state; maps are skipped because several collaborators initialise theirs lazily.
		}
	}
}
