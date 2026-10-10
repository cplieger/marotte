package translate

import (
	"context"
	"strconv"

	"github.com/cplieger/marotte/internal/marotte"
)

// nopChatRecords is a chatRecords whose every method is a no-op, for embedding in a double
// that overrides only the calls its test observes.
type nopChatRecords struct{}

func (nopChatRecords) Get(context.Context, marotte.ChatID) (*marotte.Chat, bool) { return nil, false }

func (nopChatRecords) Mutate(context.Context, marotte.ChatID, func(*marotte.Chat, bool) bool) (string, error) {
	return "", nil
}

func (nopChatRecords) PromptTexts(context.Context, marotte.ChatID) ([]string, error) {
	return nil, nil
}

func (nopChatRecords) EmptyCompactions(context.Context, marotte.ChatID) (int, error) { return 0, nil }

func (nopChatRecords) DepartedName(marotte.ChatID) (string, bool) { return "", false }

func (nopChatRecords) WaitingWorkflowMessages(context.Context, marotte.ChatID) ([]marotte.WorkflowMessage, error) {
	return nil, nil
}

var _ chatRecords = nopChatRecords{}

// recStore is a chatRecords whose every call answers err, counting header writes, so a
// test can stage a chat the store refuses and observe each write site's report.
type recStore struct {
	nopChatRecords
	err         error
	mutateCalls int
}

func (s *recStore) Mutate(_ context.Context, _ marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	s.mutateCalls++
	if s.err != nil {
		return "", s.err
	}
	if fn != nil {
		_ = fn(&marotte.Chat{}, true)
	}
	return strconv.Itoa(s.mutateCalls), nil
}

func (s *recStore) PromptTexts(context.Context, marotte.ChatID) ([]string, error) {
	return nil, s.err
}

func (s *recStore) EmptyCompactions(context.Context, marotte.ChatID) (int, error) {
	return 0, s.err
}

var _ chatRecords = (*recStore)(nil)

// nopMCPRecorder is a no-op mcpRecorder for the handler benchmarks, which drive frames
// whose MCP side effects they do not assert on.
type nopMCPRecorder struct{}

func (nopMCPRecorder) RecordConnected(context.Context, string, marotte.MCPSource, []string, []marotte.MCPPromptInfo, []marotte.MCPResourceInfo, []marotte.MCPResourceTemplateInfo) {
}

func (nopMCPRecorder) RecordOAuth(context.Context, string, marotte.MCPSource, string) {}

func (nopMCPRecorder) RecordInitFailure(context.Context, string, marotte.MCPSource, string) {}

func (nopMCPRecorder) RecordDisabled(context.Context, string, marotte.MCPSource) {}

var _ mcpRecorder = nopMCPRecorder{}

// hostDouble names every role a Translator takes, so one value fills every slot of Roles.
// Test-only: production wires each role to its own owner.
type hostDouble interface {
	Broadcaster
	pendingPermAdder
	pusher
	sessionResolver
	terminalReader
	hookStatusReader
	modelCatalog
	runOriginAccess
	runBoundsAccess
	turnInterruptAccess
	turnMetering
	chatRecords
	turnAccess
	RunAppender
	turnBoundary
	RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string)
	sentSteers
	steerBuffer
	MCPRecorder() mcpRecorder
	governanceAccess
	// A method here so rolesOf fills it per fixture.
	WorkDir() string
}

func rolesOf(d hostDouble) *Roles {
	return &Roles{
		Bus:           d,
		Chats:         d,
		Turns:         d,
		Runs:          d,
		Bracket:       d,
		Lines:         d,
		Steers:        d,
		SteerBuffer:   d,
		PendingPerms:  d,
		Push:          d,
		Sessions:      d,
		Terminals:     d,
		HookStatus:    d,
		Catalog:       d,
		WorkDir:       d.WorkDir(),
		MCP:           d.MCPRecorder(),
		Governance:    d,
		RunOrigin:     d,
		RunBounds:     d,
		TurnInterrupt: d,
		Metering:      d,
	}
}
