package marotte

// The path wildcards of the routes, one struct per wildcard set, each field named by its
// wildcard. Shared here because the generated client names a type by its Go name alone, so two
// packages declaring the same shape under one name would collide.

// IDPath is a route's {id}.
type IDPath struct {
	ID string `json:"id"`
}

// IDTurnPath is a route's {id} and {turn}.
type IDTurnPath struct {
	ID   string `json:"id"`
	Turn string `json:"turn"`
}

// IDOpPath is a route's {id} and {op}.
type IDOpPath struct {
	ID string `json:"id"`
	Op string `json:"op"`
}

// IDToolPath is a route's {id} and {tool}.
type IDToolPath struct {
	ID   string `json:"id"`
	Tool string `json:"tool"`
}

// RunStepPath is a run step route's {id} and {path}.
type RunStepPath struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// NamePath is a route's {name}.
type NamePath struct {
	Name string `json:"name"`
}

// DirPath is a route's {dir}.
type DirPath struct {
	Dir string `json:"dir"`
}

// SessionPath is a route's {session}.
type SessionPath struct {
	Session string `json:"session"`
}
