package translate

import (
	"context"
	"strconv"

	"github.com/cplieger/marotte/internal/marotte"
)

// nopChatRecords is a ChatRecords whose every method is a no-op, for embedding in a double
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

var _ ChatRecords = nopChatRecords{}

// recStore is a ChatRecords whose every call answers err, counting the header
// writes, so a test can stage a chat the store refuses (deleted, or a disk fault)
// and observe how each write site reports it.
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

var _ ChatRecords = (*recStore)(nil)

// nopMCPRecorder is a no-op MCPRecorder for the handler benchmarks, which drive frames
// whose MCP side effects they do not assert on.
type nopMCPRecorder struct{}

func (nopMCPRecorder) RecordConnected(context.Context, string, []string, []marotte.MCPPromptInfo, []marotte.MCPResourceInfo) {
}

func (nopMCPRecorder) RecordOAuth(context.Context, string, string) {}

func (nopMCPRecorder) RecordInitFailure(context.Context, string, string) {}

func (nopMCPRecorder) RecordDisabled(context.Context, string) {}

func (nopMCPRecorder) SignalReady() {}

var _ MCPRecorder = nopMCPRecorder{}

// hostDouble names every role a Translator takes, so one value fills every slot of Roles.
// It exists ONLY for the doubles here and in the handler tests; production wires each role
// to its own owner.
type hostDouble interface {
	Broadcaster
	PendingPermAdder
	Pusher
	SessionResolver
	TerminalReader
	HookStatusReader
	ModelCatalog
	RunOriginAccess
	RunBoundsAccess
	TurnInterruptAccess
	TurnMetering
	ChatRecords
	Responder
	TurnAccess
	RunAppender
	TurnBoundary
	RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string)
	SentSteers
	SteerBuffer
	MCPRecorder() MCPRecorder
	SetGovernance(state marotte.GovernanceStatePayload)
	// WorkDir is a Roles FIELD in production; the double answers it as a method so rolesOf can
	// fill that field per fixture, because relPath's table drives it.
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
		Respond:       d,
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
