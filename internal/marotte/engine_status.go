package marotte

import "time"

// EngineState is where the one agent engine stands.
type EngineState string

// The engine states, in the order a generation passes through them.
const (
	EngineStarting   EngineState = "starting"
	EngineReady      EngineState = "ready"
	EngineRestarting EngineState = "restarting"
	EngineDown       EngineState = "down"
)

// EngineReason names what a pending restart would apply: a Settings key whose engine value differs from the one the
// running engine was spawned with, or EngineReasonKiroCLIVersion or EngineReasonAccountSwitch.
type EngineReason string

const (
	// EngineReasonKiroCLIVersion means another kiro-cli version became active.
	EngineReasonKiroCLIVersion EngineReason = "kiro_cli_version"
	// EngineReasonAccountSwitch means a sign-in the running engine cannot take live is waiting for it.
	EngineReasonAccountSwitch EngineReason = "account_switch"
)

// EngineStatus is GET /api/engine's answer and the engine_status event's payload.
type EngineStatus struct {
	Since    time.Time           `json:"since"`
	LastExit *EngineExit         `json:"last_exit,omitempty"`
	State    EngineState         `json:"state"`
	Busy     EngineBusy          `json:"busy"`
	Restart  EngineRestart       `json:"restart"`
	Version  EngineStatusVersion `json:"version"`
}

// EngineStatusVersion orders snapshots that reach a reader by two paths, the GET and the event, in any order, and
// across marotte restarts: BootMS is the Unix millisecond the process's engine owner was built and Seq rises with each
// snapshot it takes. A snapshot is newer when (BootMS, Seq) is higher, compared in that order. The order across
// processes holds while the wall clock does not step back past a restart.
type EngineStatusVersion struct {
	BootMS int64  `json:"boot_ms"`
	Seq    uint64 `json:"seq"`
}

// EngineRestart is whether a restart is pending and what it would apply.
type EngineRestart struct {
	Reasons []EngineReason `json:"reasons"`
	Pending bool           `json:"pending"`
	// ConfigUnreadable: config.json could not be read at the last look, so no queued reason applies until it can;
	// Reasons stay queued.
	ConfigUnreadable bool `json:"config_unreadable"`
}

// EngineBusy is what a restart now would interrupt.
type EngineBusy struct {
	Turns []EngineBusyTurn `json:"turns"`
	Runs  []EngineBusyRun  `json:"runs"`
}

// EngineBusyTurn is a chat whose reply a restart would stop.
type EngineBusyTurn struct {
	ChatID ChatID `json:"chat_id"`
	Name   string `json:"name"`
}

// EngineBusyRun is a workflow run a restart would pause and resume. Name is its display name
// (RunDisplayName), "" when nothing names it yet; ChatName is its launching chat's, "" for a
// parentless run.
type EngineBusyRun struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name"`
	ChatName   string `json:"chat_name,omitempty"`
}

// EngineExit is the engine's last end: its cause and when.
type EngineExit struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}
