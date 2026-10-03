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
		Bus:       rt.bus,
		Tabs:      tabSetOrNil(rt.tabs),
		Runs:      rt.runs,
		RunCutter: rt.runs,
		Effort:    rt.coord,
		Modes:     rt.coord,
		Teardown:  rt,
		Perms:     rt.bus,
		Terminals: rt.agentTerms,
		Workspace: command.Workspace{
			Dir:       rt.lifecycle.workDir,
			ConfigDir: rt.lifecycle.configDir,
			// An uploaded file's path comes back as an attachment, and the composer's
			// upload target sits beside the workspace rather than inside it, so
			// attachment confinement needs it as a second root or every composer
			// drop degrades to a text block naming the filename.
			UploadsDir: marotte.DefaultUploadDir,
		},
		Lifecycle:     rt.lifecycle,
		MCP:           rt.mcpRegistry,
		Admission:     rt,
		TurnOutcome:   rt,
		Steers:        rt.steerLedger,
		SteerQueue:    rt.steerQueue,
		SteerJobs:     rt.steerQueue,
		Status:        rt,
		AuthReadiness: rt.authReadiness,
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

// specApprovalsOrNil keeps a nil store a nil INTERFACE, tabSetOrNil's reason: an
// interface holding a typed nil is not nil, so the handler's unavailable branch
// would never be taken and it would nil-deref instead.
func specApprovalsOrNil(st *specapproval.Store) command.SpecApprovals {
	if st == nil {
		return nil
	}
	return st
}

// Membership returns the coordinator over the chat store and the open-tab
// set, retention's handle on the open-tab predicate and the post-purge close.
func (rt *Runtime) Membership() *command.Membership { return rt.membership }
