package command

// Rewind reverts the chat you are in (a fork makes a second chat):
// `_kiro/checkpoint/revertMultiple` drops the addressed user message and everything after it, rolls
// the files back, and tombstones the cut.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
)

// reasonRunsInCut is the 409 refusal class for a cut holding a live run's launch
// that the reader has not confirmed; the envelope names the runs.
const reasonRunsInCut = "runs_in_cut"

// errRewindRunsInCut is the refusal's prose; the runs ride beside it.
var errRewindRunsInCut = errors.New("rewinding here stops workflow runs this conversation launched. Confirm to stop them and rewind")

// CmdRewindChat reverts the chat to a past turn via KAS's checkpoint machinery, then appends the
// turn_revert that records it. The record is required: the merge keeps a record turn the replay
// lacks, and the appended revert refuses a projection built from the pre-revert replay. Refused
// while the registry holds a turn or reservation; a live run the cut launched is stopRunsInCut's
// question, asked first.
func CmdRewindChat(
	ctx context.Context,
	bridges BridgeAccess,
	chats ChatStore,
	admission TurnAdmission,
	runs RunCutter,
	bus Broadcaster,
	cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.RewindChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.MessageID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	chat, ok := chats.Get(ctx, cmd.ChatID)
	if !ok {
		return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
	}
	if _, held := admission.AdmissionHolderSource(cmd.ChatID); held {
		return nil, StatusError(http.StatusConflict, errRewindTurnOpen)
	}
	target, found, err := chats.RewindTarget(ctx, cmd.ChatID, p.MessageID)
	if err != nil {
		slog.Error("rewind: resolve target", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if !found {
		return nil, StatusError(http.StatusBadRequest, errRewindTargetNotFound)
	}
	if refused := stopRunsInCut(ctx, runs, cmd.ChatID, target.LaunchedRuns, p.Confirmed); refused != nil {
		return nil, refused
	}

	bridge, err := resumeForRevert(ctx, bridges, cmd.ChatID, chat.ACPSessionID)
	if err != nil {
		return nil, err
	}

	result, status, err := revertToMessage(ctx, bridge, cmp.Or(target.KASMessageID, p.MessageID))
	if err != nil {
		slog.Warn("rewind: revert failed", "chat", cmd.ChatID, "status", status,
			"kas_id_known", target.KASMessageID != "", keyError, err)
		return nil, StatusError(status, explainRevertRefusal(target.KASMessageID, err))
	}

	// KAS slices from the addressed prompt inclusive, so the record is AT the turn. It is an
	// append, so a failed revert loses nothing and a later record can answer one that failed after
	// this landed.
	record, minted, err := chats.Revert(ctx, cmd.ChatID, target.Turn, target.KASMessageID)
	if err != nil {
		slog.Error("rewind: record the revert", "chat", cmd.ChatID, "turn", target.Turn, keyError, err)
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	// The carrier goes out FIRST when the log minted one: the record names it as its
	// turn, and a client that meets the record first answers a turn it does not hold
	// as a hole.
	for _, m := range minted {
		translate.PublishAppended(ctx, bus, cmd.ChatID, "", m)
	}
	translate.PublishAppended(ctx, bus, cmd.ChatID, "", record)

	slog.Info("chat rewound",
		"chat", cmd.ChatID, "message", p.MessageID, "turn", target.Turn,
		"carrier", record.Turn, "carrier_minted", len(minted) > 0,
		"restored_files", len(result.AffectedFiles), "total_files", result.TotalFiles)
	return responseWith(map[string]any{
		"restored_files": result.AffectedFiles,
	}), nil
}

// stopRunsInCut is the rewind-versus-live-run rule: a run launched inside the cut is un-said, so an
// unconfirmed rewind answers 409 naming those runs, and a confirmed one cancels each and waits
// BEFORE the revert so no step appends into the cut. A run still live after its wait is logged and
// the rewind continues: the cancel landed, and a step's entries are the run's own log.
func stopRunsInCut(ctx context.Context, runs RunCutter, chatID marotte.ChatID, launched []string, confirmed bool) error {
	live := runs.LiveRuns(launched)
	if len(live) == 0 {
		return nil
	}
	if !confirmed {
		return StatusErrorRuns(http.StatusConflict, reasonRunsInCut, live, errRewindRunsInCut)
	}
	for _, run := range live {
		switch err := runs.CancelRun(ctx, run.ID); {
		case err == nil:
			slog.Info("rewind: stopped a run the cut launched", "chat", chatID, "workflow_id", run.ID, "label", run.Label)
		case errors.Is(err, ErrRunStillLive):
			slog.Warn("rewind: a run the cut launched was told to stop but is still live", "chat", chatID, "workflow_id", run.ID)
		default:
			slog.Warn("rewind: cancel of a run the cut launched failed", "chat", chatID, "workflow_id", run.ID, keyError, err)
			return StatusError(http.StatusBadGateway, fmt.Errorf("stopping run %s failed: %w", run.ID, err))
		}
	}
	return nil
}

// resumeForRevert hands back a bridge whose session is the one the target turn
// lives in. `want` is read BEFORE the resume, because a failed session/load falls
// through to session/new and retires that id. No replay barrier: a swap landing
// after the revert is refused by the record the revert appended.
func resumeForRevert(ctx context.Context, bridges BridgeAccess, chatID marotte.ChatID, want string) (Bridge, error) {
	if want == "" {
		return nil, StatusError(http.StatusConflict, errRewindNoSession)
	}

	// Empty model on purpose: spawnBridge then keeps the chat's own, so a rewind
	// can never silently change which model the chat runs.
	bridge, err := bridges.OpenBridge(ctx, chatID, "")
	if err != nil || bridge == nil {
		// The spawn's own error is logged, not forwarded: it names marotte's
		// internals, and the client renders the reason verbatim to the user.
		slog.Warn("rewind: no bridge to revert on", "chat", chatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, errRewindNoBridge)
	}
	if string(bridge.SessionID()) != want {
		// The resume fell through to a fresh session, which holds none of this
		// transcript: KAS would refuse the id or roll back the wrong thing.
		slog.Warn("rewind: original session not resumed",
			"chat", chatID, "want", want, "got", bridge.SessionID())
		return nil, StatusError(http.StatusConflict, errRewindSessionNotResumed)
	}
	return bridge, nil
}

// revertResult is KAS's reply to a revert. affectedFiles are the paths it put
// back (or removed, for a file the discarded turns created).
type revertResult struct {
	Error         string   `json:"error"`
	AffectedFiles []string `json:"affectedFiles"`
	TotalFiles    int      `json:"totalFiles"`
	Success       bool     `json:"success"`
}

// revertToMessage performs the KAS round trip and normalises its two failure
// channels into one error plus the status to report: a transport failure comes
// back as an error, a refusal KAS can explain as `success:false` with a reason,
// forwarded verbatim since it is more specific than anything marotte can infer.
func revertToMessage(ctx context.Context, bridge sessionCaller, messageID string) (revertResult, int, error) {
	var result revertResult
	resp, err := bridge.Call(ctx, marotte.MethodCheckpointRevertMultiple, SessionParams(bridge, map[string]any{
		"messageId": messageID,
	}))
	if err != nil {
		return result, http.StatusBadGateway, err
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}
	if !result.Success {
		return result, http.StatusConflict, errors.New(cmp.Or(result.Error, "revert failed"))
	}
	return result, http.StatusOK, nil
}

// explainRevertRefusal appends marotte's account of an unaddressable turn to a refusal KAS could
// not explain. It keys on the fallback having been taken, since the reply carries no code; it
// appends rather than replaces, so a more specific reason survives.
func explainRevertRefusal(kasMessageID string, err error) error {
	if kasMessageID != "" {
		return err
	}
	return fmt.Errorf("%w — %w", err, errRewindNoAgentID)
}

// CmdSetEffort sets the chat's reasoning-effort level: switched in place on a running session,
// persisted on the chat and applied to later sessions through StartOpts.Effort. A bridgeless chat
// auto-creates like CmdSetMode. The level is recorded LAST as the new-chat seed for this model, so
// a refused level is never remembered.
func CmdSetEffort(
	ctx context.Context,
	bridges BridgeAccess,
	chats ChatStore,
	bus Broadcaster,
	ws Workspace,
	recorder EffortRecorder,
	cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetEffortCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || !p.Level.Valid() {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	// Picking any tier turns thinking back on, the slider's other half of its Off
	// stop. Before the effort, because KAS caps a high tier while thinking is off.
	thinkingOff := false
	if rec, ok := chats.Get(ctx, cmd.ChatID); ok && rec.ThinkingIsOff(recorder.ThinkingDefaultOff(rec.Model)) {
		thinkingOff = true
		if err := setThinking(ctx, bridges, cmd.ChatID, marotte.ThinkingOn); err != nil {
			return nil, err
		}
	}

	// Fail fast so a refusal is reported, not persisted; a cold-spawning bridge is not a refusal
	// (applySessionConfig).
	if err := applySessionConfig(ctx, bridges, cmd.ChatID, "set_effort",
		marotte.MethodSetConfigOption, configOptionParams(marotte.ConfigOptionEffort, string(p.Level))); err != nil {
		return nil, err
	}

	model, changed, err := persistEffortPick(ctx, chats, cmd.ChatID, string(p.Level), thinkingOff)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}

	slog.Info("effort set", "chat", cmd.ChatID, "level", p.Level)
	if changed {
		recorder.PersistEffortChange(ctx, cmd.ChatID, model, p.Level)
	}
	recordEffortSeed(ctx, bus, ws.ConfigDir, model, p.Level)
	return responseWith(map[string]any{"level": p.Level}), nil
}

// persistEffortPick writes level onto the chat record, auto-creating it, and turns
// thinking back on when the pick did. It reports the record's model, which the
// payload does not carry, and whether the level changed.
func persistEffortPick(
	ctx context.Context, chats ChatStore, chatID marotte.ChatID, level string, thinkingOff bool,
) (model string, changed bool, err error) {
	_, err = chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		model = c.Model
		thinkingChanged := false
		if thinkingOff && c.Thinking != marotte.ThinkingOn {
			c.Thinking = marotte.ThinkingOn
			thinkingChanged = true
		}
		if !exists {
			c.Name = marotte.DefaultChatName
			c.Effort = level
			changed = true
			return true
		}
		if c.Effort == level {
			return thinkingChanged
		}
		c.Effort = level
		changed = true
		return true
	})
	return model, changed, err
}

// recordEffortSeed remembers level as what a NEW chat on model opens with, and tells the other
// devices. A failure does not fail the command; a chat with no model is skipped.
func recordEffortSeed(ctx context.Context, bus Broadcaster, configDir, model string, level marotte.EffortLevel) {
	if configDir == "" || model == "" {
		return
	}
	if _, err := settings.Update(ctx, configDir, func(doc map[string]json.RawMessage) error {
		byModel := map[string]string{}
		if raw, ok := doc[settings.KeyLastEffortByModel]; ok {
			// A value that does not decode is one the effective-settings reader
			// already ignores, so replacing it repairs the key rather than losing
			// a level anything could read.
			if err := json.Unmarshal(raw, &byModel); err != nil {
				byModel = map[string]string{}
			}
		}
		byModel[model] = string(level)
		raw, err := json.Marshal(byModel)
		if err != nil {
			return err
		}
		doc[settings.KeyLastEffortByModel] = raw
		return nil
	}); err != nil {
		slog.Warn("effort seed not recorded", "model", model, "level", level, keyError, err)
		return
	}
	bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSettingsUpdated, "", marotte.SettingsUpdatedPayload{}))
}
