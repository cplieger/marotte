package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/ruleengine"
)

// ruleTaps is the rule engine's turn taps. turnID is the turn_open entry's id, never the lifecycle's Seq, which
// starts again with every life; caller is the opening prompt's, nil for a turn no caller's prompt opened.
type ruleTaps interface {
	TurnOpened(chatID marotte.ChatID, turnID string, caller *marotte.Caller)
	TurnClosed(chatID marotte.ChatID, turnID string, caller *marotte.Caller)
}

// ruleEngine is the rule engine as the runtime wires it: its taps, the Bot commands' way in, and the setup's view.
type ruleEngine interface {
	ruleTaps
	command.RuleEngine
	Status(chatID marotte.ChatID) ruleengine.Status
}

var _ ruleEngine = (*ruleengine.Engine)(nil)

// SetRuleEngine wires the rule engine into the turn taps, the Bot commands, the tab coordinator's closes and reopens,
// and the Bot setup route. A setter because the engine delivers through this runtime; call it before the runtime
// serves.
func (rt *Runtime) SetRuleEngine(e ruleEngine) {
	rt.rules = e
	rt.coord.turns.rules = e
	rt.dispatcher.SetRuleEngine(e)
	if rt.membership != nil {
		rt.membership.SetRuleIndex(e)
	}
}

// RuleDeps answers the rule engine's dependencies on this runtime, over store: the notify door's delivery, the drain
// and readiness gates the HTTP notify door is built with, and each chat's turn and waiting state.
func (rt *Runtime) RuleDeps(store ruleengine.Store) ruleengine.Deps {
	return ruleengine.Deps{
		Store:    store,
		Notifier: ruleDoor{rt: rt},
		Enter:    rt.lifecycle.enterUnlessDraining,
		Ready:    rt.engine.isReady,
		TurnLive: rt.TurnLive,
		Waiting:  rt.bus.chatStatus.waiting,
	}
}

// ruleDoor is the rule engine's way into a Bot's chat.
type ruleDoor struct{ rt *Runtime }

func (d ruleDoor) Notify(ctx context.Context, chatID marotte.ChatID, text string, from *marotte.Caller,
	opts command.NotifyOptions,
) (string, error) {
	return d.rt.dispatcher.Notify(ctx, chatID, text, from, opts)
}

func (d ruleDoor) Unqueue(ctx context.Context, chatID marotte.ChatID, messageID string) error {
	return d.rt.Unqueue(ctx, chatID, messageID)
}

// promptCaller is p's caller, nil for no prompt.
func promptCaller(p *marotte.EntryPrompt) *marotte.Caller {
	if p == nil {
		return nil
	}
	return p.Caller
}
