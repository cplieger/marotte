package agent

import (
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/marotte/internal/tabs"
)

// registerCommandHandlers populates the dispatcher with the dispatch table.
func (rt *Runtime) registerCommandHandlers() {
	rt.membership = command.RegisterDefaults(rt.dispatcher, &command.Roles{
		Bridges:   bridgeRole{coord: rt.coord},
		Chats:     rt.chatStore,
		Sessions:  rt.chatStore,
		Bus:       rt.bus,
		Tabs:      tabSetOrNil(rt.tabs),
		Runs:      rt.runs,
		RunCutter: rt.runs,
		Effort:    rt.coord,
		Modes:     rt.coord,
		Teardown:  rt,
		Perms:     rt.bus,
		Terminals: rt.agentTerms,
		Stops:     rt,
		Workspace: command.Workspace{
			Terminal:  rt.shellMgr,
			Dir:       rt.lifecycle.workDir,
			ConfigDir: rt.lifecycle.configDir,
			// Composer uploads land beside the workspace, so attachment confinement needs this second root.
			UploadsDir: marotte.DefaultUploadDir,
		},
		Lifecycle:     rt.lifecycle,
		Admission:     rt,
		TurnOutcome:   rt,
		Steers:        rt.steerLedger,
		Queue:         rt.coord,
		SteerQueue:    rt.steerQueue,
		SteerJobs:     rt.steerQueue,
		Status:        rt,
		AuthReadiness: rt.authReadiness,
		Compactor:     rt,
		Renamer:       rt,
		SpecApprovals: specApprovalsOrNil(rt.specApprovals),
	})

	rt.dispatcher.Register(marotte.CmdSwitchModel, rt.cmdSwitchModel)
}

func tabSetOrNil(st *tabs.Store) command.TabSet {
	if st == nil {
		return nil
	}
	return st
}

// specApprovalsOrNil keeps a nil store a nil interface, or the handler's unavailable branch nil-derefs.
func specApprovalsOrNil(st *specapproval.Store) command.SpecApprovals {
	if st == nil {
		return nil
	}
	return st
}

// Membership returns the coordinator over the chat store and open-tab set, for retention.
func (rt *Runtime) Membership() *command.Membership { return rt.membership }
