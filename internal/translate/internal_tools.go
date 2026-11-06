package translate

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// isInternalTool reports engine bookkeeping KAS announces as tool calls, which marotte never draws as
// cards (kiro-cli's terminal chat folds them into a notice). The create frame is dropped before it can
// open a turn.
func isInternalTool(toolID string) bool {
	return toolID == "fetch_cloud_config"
}

// maxInternalCalls bounds the held ids: each leaves on its terminal frame, so only a pull that never
// settles (an engine death) lingers.
const maxInternalCalls = 64

// internalCalls holds the ids of the internal calls whose create was dropped, with the chat each
// arrived on, so their settle is recognised before it can reach a turn.
type internalCalls struct {
	by    map[string]marotte.ChatID
	order []string
	mu    sync.Mutex
}

func (c *internalCalls) add(id string, chatID marotte.ChatID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = make(map[string]marotte.ChatID)
	}
	if _, ok := c.by[id]; ok {
		return
	}
	if len(c.order) == maxInternalCalls {
		delete(c.by, c.order[0])
		c.order = c.order[1:]
	}
	c.by[id] = chatID
	c.order = append(c.order, id)
}

func (c *internalCalls) holds(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.by[id]
	return ok
}

func (c *internalCalls) take(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.by[id]; !ok {
		return
	}
	delete(c.by, id)
	for i, held := range c.order {
		if held == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// settleInternalCall takes the settled call and tells the reader its outcome when it is worth telling.
func (t *Translator) settleInternalCall(ctx context.Context, chatID marotte.ChatID, tu *acpToolCallUpdateWire) {
	t.internalCalls.take(tu.ToolCallID)
	n, ok := cloudConfigNotice(tu)
	if !ok {
		return
	}
	n.ChatName = t.noticeChatName(ctx, chatID)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSystemNotice, chatID, n))
}

// acpCloudConfigPull is the cloud config pull's completed rawOutput (KAS's pullCardSettlement): Notice is
// set for duplicate paths and for a sync turned off with its replica removed.
type acpCloudConfigPull struct {
	Kind       string `json:"kind"`
	Notice     string `json:"notice"`
	Downloaded int    `json:"downloaded"`
	Deleted    int    `json:"deleted"`
}

// cloudConfigNotice is the notice a settled cloud config pull earns: a failure's own sentence, KAS's
// notice, or a count of synced files. An up-to-date pull, or a shape marotte cannot read, says nothing,
// since every chat's session open pulls.
func cloudConfigNotice(tu *acpToolCallUpdateWire) (marotte.SystemNoticePayload, bool) {
	if tu.Status == marotte.ToolFailed {
		var msg string
		if json.Unmarshal(tu.RawOutput, &msg) != nil || msg == "" {
			return marotte.SystemNoticePayload{}, false
		}
		return marotte.SystemNoticePayload{Level: marotte.NoticeWarning, Message: displayText(msg)}, true
	}
	var pull acpCloudConfigPull
	if tu.Status != marotte.ToolCompleted || json.Unmarshal(tu.RawOutput, &pull) != nil {
		return marotte.SystemNoticePayload{}, false
	}
	if pull.Notice != "" {
		return marotte.SystemNoticePayload{Level: marotte.NoticeWarning, Message: displayText(pull.Notice)}, true
	}
	changed := pull.Downloaded + pull.Deleted
	if pull.Kind != "synced" || changed <= 0 {
		return marotte.SystemNoticePayload{}, false
	}
	files := " files"
	if changed == 1 {
		files = " file"
	}
	return marotte.SystemNoticePayload{
		Level:   marotte.NoticeInfo,
		Message: "Cloud config synced: " + strconv.Itoa(changed) + files + " updated.",
	}, true
}
