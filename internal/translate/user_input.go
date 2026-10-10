package translate

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
)

// HandleUserInput processes a _kiro/userInput request (KAS 2.14+, advertised through the
// _meta.kiro.userInput capability): it surfaces a question dialog whose reply
// CmdUserInputResponse sends. The correlation id is msg.ID, and the pending tracker
// replays the dialog on reconnect. KAS completes the matching user_input tool_call itself.
func (t *Translator) HandleUserInput(ctx context.Context, chatID marotte.ChatID, origin AskOrigin, msg *marotte.RPCResponse) {
	if msg.ID == nil {
		// Without an id no response can be routed, and KAS would stall the question forever.
		slog.Warn("user input request missing id", "chat_id", chatID)
		return
	}
	type userInputParams struct {
		SessionID  string                `json:"sessionId"`
		ToolCallID string                `json:"toolCallId"`
		Question   string                `json:"question"`
		Options    []wireUserInputOption `json:"options"`
	}
	reqID := *msg.ID
	p, err := decodeParams[userInputParams](msg)
	if err != nil {
		refuseAsk(ctx, chatID, origin, marotte.MethodKiroUserInput, reqID,
			marotte.UserInputResult{Action: marotte.UserInputActionDismissed}, err)
		return
	}

	options := sanitizeUserInputOptions(p.Options)

	subSessionID := t.deriveSubSession(chatID, p.SessionID)

	step := t.steps.refFor(p.SessionID)
	question := displayText(p.Question)
	evt := marotte.NewEvent(marotte.EventUserInputNeeded, chatID, marotte.UserInputNeededPayload{
		// The question is model-composed and unbounded on the wire; same rule as a permission title.
		Question:     question,
		Options:      options,
		ToolCallID:   p.ToolCallID,
		SubSessionID: subSessionID,
		RunID:        step.WorkflowID,
		NodeID:       step.NodeID,
	})
	t.bus.Broadcast(ctx, t.pendingPerms.PendingPermsAdd(reqID, evt, origin))
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID, marotte.WorkingLabelPayload{Label: marotte.WorkingLabelInput}))
	n := notice.Question(t.push.NoticeTarget(ctx, chatID, step.WorkflowID),
		step.NodeID, question)
	t.push.Notify(ctx, chatID, &n)
}

// wireUserInputOption / wireUserInputSubOption are KAS's `_kiro/userInput` option shapes.
type wireUserInputSubOption struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type wireUserInputOption struct {
	Title           string                   `json:"title"`
	Description     string                   `json:"description"`
	SubOptionsLabel string                   `json:"subOptionsLabel"`
	SubOptions      []wireUserInputSubOption `json:"subOptions"`
	Recommended     bool                     `json:"recommended"`
}

// Bounds on what a userInput question may put on screen: a long list pushes the composer
// off screen, an empty title answers empty text, and duplicate titles make the answer
// ambiguous (the reply carries the title, not an index).
const (
	maxUserInputOptions    = 24
	maxUserInputSubOptions = 24
)

// sanitizeUserInputOptions drops what cannot be answered, defuses what is shown, and bounds
// the rest. displayText runs before TrimSpace and the dedup: trimming first leaves a blank
// card that answers with an invisible control, and dedup on the raw form lets two visually
// identical titles survive. The title IS the answer the agent receives.
func sanitizeUserInputOptions(in []wireUserInputOption) []marotte.UserInputOption {
	options := make([]marotte.UserInputOption, 0, min(len(in), maxUserInputOptions))
	seen := make(map[string]struct{}, len(in))
	for i := range in {
		o := &in[i]
		title := strings.TrimSpace(displayText(o.Title))
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		options = append(options, marotte.UserInputOption{
			Title:           title,
			Description:     displayText(o.Description),
			SubOptionsLabel: displayText(o.SubOptionsLabel),
			SubOptions:      sanitizeUserInputSubOptions(o.SubOptions),
			Recommended:     o.Recommended,
		})
		if len(options) == maxUserInputOptions {
			break
		}
	}
	return options
}

// sanitizeUserInputSubOptions applies the same rules one level down.
func sanitizeUserInputSubOptions(in []wireUserInputSubOption) []marotte.UserInputSubOption {
	subs := make([]marotte.UserInputSubOption, 0, min(len(in), maxUserInputSubOptions))
	seen := make(map[string]struct{}, len(in))
	for _, sub := range in {
		title := strings.TrimSpace(displayText(sub.Title))
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		subs = append(subs, marotte.UserInputSubOption{Title: title, Description: displayText(sub.Description)})
		if len(subs) == maxUserInputSubOptions {
			break
		}
	}
	return subs
}
