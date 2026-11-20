package translate

// Background jobs of a session with kiro-cli's job shell on. KAS owns the jobs: marotte keeps only
// what it shows. _kiro/job/update feeds each chat's running list (KAS replays the running ones on
// attach and load); a job's end is filed once as a job_ended entry, from the card's end update or
// KAS's job notice, whichever arrives first; _kiro/tools/content_chunk streams a running call's
// output into its card.

import (
	"cmp"
	"context"
	"encoding/json"
	"html"
	"regexp"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/turnlog"
)

// maxJobEndedOutput bounds the result tail a job_ended entry keeps; the whole log stays readable
// through _kiro/job/output while the session lives.
const maxJobEndedOutput = 16 << 10

// maxJobEndClaims bounds a chat's remembered ended jobs. An evicted one only risks a second row for
// a job whose notice arrives after this many later jobs ended.
const maxJobEndClaims = 256

type chatJobs struct {
	running map[string]marotte.BackgroundJobPayload
	// described keeps a running job's description for its end's row after the job leaves running.
	described map[string]string
	// cards maps a job to the tool call kiro-cli moved into it, so an end from the notice names its card.
	cards   map[string]string
	session string
	// ended is the jobs whose end was filed, oldest first.
	ended []string
}

// jobRegistry is every chat's running jobs and filed ends, safe for concurrent use.
type jobRegistry struct {
	byChat map[marotte.ChatID]*chatJobs
	mu     sync.Mutex
}

func (r *jobRegistry) chatLocked(chatID marotte.ChatID) *chatJobs {
	if r.byChat == nil {
		r.byChat = map[marotte.ChatID]*chatJobs{}
	}
	c := r.byChat[chatID]
	if c == nil {
		c = &chatJobs{
			running: map[string]marotte.BackgroundJobPayload{}, described: map[string]string{}, cards: map[string]string{},
		}
		r.byChat[chatID] = c
	}
	return c
}

// update applies one job/update; a frame from another session than the held rows' replaces them,
// since a chat's jobs die with its session.
func (r *jobRegistry) update(chatID marotte.ChatID, session string, p *marotte.BackgroundJobPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.chatLocked(chatID)
	if session != "" && c.session != session {
		c.session = session
		clear(c.running)
		clear(c.described)
		clear(c.cards)
	}
	// A claimed end has had (or will have) its row's facts dropped by settleEnd; a later frame must not restore them.
	if p.Description != "" && !slices.Contains(c.ended, p.ID) {
		c.described[p.ID] = p.Description
	}
	if p.Status == marotte.BackgroundJobRunning {
		c.running[p.ID] = *p
		return
	}
	delete(c.running, p.ID)
}

// clearRunning drops a chat's running rows and answers them, oldest first.
func (r *jobRegistry) clearRunning(chatID marotte.ChatID) []marotte.BackgroundJobPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.byChat[chatID]
	if c == nil {
		return nil
	}
	out := sortedJobs(c.running)
	clear(c.running)
	return out
}

// claimEnd reports whether jobID's end is still unfiled, and marks it filed.
func (r *jobRegistry) claimEnd(chatID marotte.ChatID, jobID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.chatLocked(chatID)
	if slices.Contains(c.ended, jobID) {
		return false
	}
	if len(c.ended) == maxJobEndClaims {
		c.ended = slices.Delete(c.ended, 0, 1)
	}
	c.ended = append(c.ended, jobID)
	return true
}

// settleEnd closes jobID's claim: a filed end drops what the chat held for the job's row, and an unfiled one
// releases the claim so the job's other end source can file it.
func (r *jobRegistry) settleEnd(chatID marotte.ChatID, jobID string, filed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.chatLocked(chatID)
	if filed {
		delete(c.described, jobID)
		delete(c.cards, jobID)
		return
	}
	c.ended = slices.DeleteFunc(c.ended, func(id string) bool { return id == jobID })
}

func (r *jobRegistry) noteCard(chatID marotte.ChatID, jobID, toolCallID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.chatLocked(chatID); !slices.Contains(c.ended, jobID) {
		c.cards[jobID] = toolCallID
	}
}

// known is what the chat holds about a job for its end's row: its description and its card.
func (r *jobRegistry) known(chatID marotte.ChatID, jobID string) (description, toolCallID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.byChat[chatID]; c != nil {
		return c.described[jobID], c.cards[jobID]
	}
	return "", ""
}

func (r *jobRegistry) forget(chatID marotte.ChatID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byChat, chatID)
}

// ChatJobs is one chat's running jobs and the session they run in.
type ChatJobs struct {
	Session string
	Jobs    []marotte.BackgroundJobPayload
}

// RunningJobs is every chat holding a running job.
func (t *Translator) RunningJobs() map[marotte.ChatID]ChatJobs {
	r := t.jobs
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[marotte.ChatID]ChatJobs, len(r.byChat))
	for id, c := range r.byChat {
		if len(c.running) > 0 {
			out[id] = ChatJobs{Session: c.session, Jobs: sortedJobs(c.running)}
		}
	}
	return out
}

// DropJob removes a running row kiro-cli no longer knows and announces it ended with no known outcome.
func (t *Translator) DropJob(ctx context.Context, chatID marotte.ChatID, jobID string) {
	r := t.jobs
	r.mu.Lock()
	var j marotte.BackgroundJobPayload
	held := false
	if c := r.byChat[chatID]; c != nil {
		j, held = c.running[jobID]
		delete(c.running, jobID)
	}
	r.mu.Unlock()
	if held {
		j.Status = marotte.BackgroundJobOutcomeUnknown
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventJobUpdate, chatID, j))
	}
}

// ForgetChatJobs drops a deleted chat's job state.
func (t *Translator) ForgetChatJobs(chatID marotte.ChatID) { t.jobs.forget(chatID) }

func sortedJobs(m map[string]marotte.BackgroundJobPayload) []marotte.BackgroundJobPayload {
	out := make([]marotte.BackgroundJobPayload, 0, len(m))
	for _, j := range m {
		out = append(out, j)
	}
	slices.SortFunc(out, func(a, b marotte.BackgroundJobPayload) int {
		return cmp.Or(cmp.Compare(a.StartedAt, b.StartedAt), cmp.Compare(a.ID, b.ID))
	})
	return out
}

type kasJobUpdate struct {
	SessionID   string `json:"sessionId"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Status      string `json:"status"`
	StartedAt   string `json:"startedAt"`
	EndedAt     string `json:"endedAt"`
}

// HandleJobUpdate keeps a chat's running list and announces the change as job_update.
func (t *Translator) HandleJobUpdate(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[kasJobUpdate](msg, "job/update")
	if !ok || p.ID == "" || t.foreignSession(chatID, p.SessionID) {
		return
	}
	job := marotte.BackgroundJobPayload{
		ID: displayText(p.ID), Kind: displayText(p.Kind), Description: displayText(p.Description),
		Status: JobState(p.Status), StartedAt: displayText(p.StartedAt), EndedAt: displayText(p.EndedAt),
	}
	t.jobs.update(chatID, p.SessionID, &job)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventJobUpdate, chatID, job))
}

// noteUnattended takes KAS's word that no job of the chat runs (activity.unattended false), which is
// all a disposed session sends: rows still held are announced ended with no known outcome.
func (t *Translator) noteUnattended(ctx context.Context, chatID marotte.ChatID, unattended bool) {
	if unattended {
		return
	}
	for _, j := range t.jobs.clearRunning(chatID) {
		j.Status = marotte.BackgroundJobOutcomeUnknown
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventJobUpdate, chatID, j))
	}
}

// fileJobEnd files a job's end once, in the chat's open turn when one is open, else after its newest turn. A failed
// write leaves the end to the job's other source.
func (t *Translator) fileJobEnd(ctx context.Context, chatID marotte.ChatID, e *marotte.EntryJobEnded) {
	if !t.jobs.claimEnd(chatID, e.JobID) {
		return
	}
	desc, card := t.jobs.known(chatID, e.JobID)
	e.Description = cmp.Or(e.Description, desc)
	e.ToolCallID = cmp.Or(e.ToolCallID, card)
	filed := t.appendLaneless(durable.Context(ctx), chatID, marotte.EntryKindJobEnded, marotte.JobEndedID(e.JobID), *e,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) { return turn.JobEnded(ctx, e) })
	t.jobs.settleEnd(chatID, e.JobID, filed)
}

// jobEndOfCall files the end a settled call's fold carries.
func (t *Translator) jobEndOfCall(ctx context.Context, chatID marotte.ChatID, tc *marotte.ToolCall) {
	if tc.Job == nil || tc.Job.End == nil {
		return
	}
	t.fileJobEnd(ctx, chatID, &marotte.EntryJobEnded{
		JobID: tc.Job.ID, ToolCallID: tc.ID, Output: outputTail(tc.Output), End: *tc.Job.End,
	})
}

// lateJobEnd files a job's end that reached a card its turn already sealed.
func (t *Translator) lateJobEnd(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, tu *acpToolCallUpdateWire, content toolUpdateContent) {
	job := jobFrom(tu.Meta.Kiro.Job)
	if attr.Step || job == nil || job.End == nil {
		return
	}
	t.fileJobEnd(ctx, chatID, &marotte.EntryJobEnded{
		JobID: job.ID, ToolCallID: tu.ToolCallID,
		Output: outputTail(toolUpdateOutput("", tu.Status, tu, content)), End: *job.End,
	})
}

// jobNoticeDescription reads description="…" off the notice's <notification> tag, which KAS escapes
// as XML attribute text (KAS's noticeOf).
var jobNoticeDescription = regexp.MustCompile(`<notification [^>]*\bdescription="([^"]*)"`)

// HandleJobNotice files the end KAS's job notice reports: a user_message_chunk whose notification is a
// system-notification naming a job. It is never a steer; any other user chunk is ignored.
func (t *Translator) HandleJobNotice(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var c acpChunkWire
	if attr.Step || json.Unmarshal(raw, &c) != nil {
		return
	}
	if end, ok := jobNoticeOf(&c.Meta, c.Content.Text); ok {
		t.fileJobEnd(ctx, chatID, &end)
	}
}

// jobNoticeOf is the job_ended entry a job notice row states; false when the row is not one.
func jobNoticeOf(meta *acpKiroMeta, text string) (marotte.EntryJobEnded, bool) {
	n := &meta.Kiro.Notification
	if n.Kind != notificationKindSystem || n.JobID == "" {
		return marotte.EntryJobEnded{}, false
	}
	e := marotte.EntryJobEnded{JobID: displayText(n.JobID)}
	if end := jobEndFrom(n.JobEnd); end != nil {
		e.End = *end
	} else {
		e.End.Status = marotte.BackgroundJobOutcomeUnknown
	}
	if m := jobNoticeDescription.FindStringSubmatch(text); m != nil {
		e.Description = displayText(html.UnescapeString(m[1]))
	}
	return e, true
}

// notificationKindSystem is KAS's notification kind for a row Kiro wrote on its own account.
const notificationKindSystem = "system-notification"

// outputTail keeps the last maxJobEndedOutput bytes, on a rune boundary.
func outputTail(s string) string {
	if len(s) <= maxJobEndedOutput {
		return s
	}
	s = s[len(s)-maxJobEndedOutput:]
	for s != "" && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

type kasContentChunk struct {
	SessionID  string `json:"sessionId"`
	ToolCallID string `json:"toolCallId"`
	Content    struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"content"`
}

// HandleToolContentChunk appends a running call's streamed output to its card in the chat's open turn.
// A chunk for a call whose turn closed is dropped: the jobs tracker's Show output reads a running job.
func (t *Translator) HandleToolContentChunk(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[kasContentChunk](msg, "tools/content_chunk")
	if !ok || p.ToolCallID == "" || p.Content.Content.Type != "text" || t.foreignSession(chatID, p.SessionID) {
		return
	}
	text := sanitize.Output(p.Content.Content.Text)
	if text == "" {
		return
	}
	turn, ok := t.turns.OwnTurn(chatID)
	if !ok {
		return
	}
	before, after, ok := turn.FoldStreamed(p.ToolCallID, func(tc *marotte.ToolCall) {
		tc.Output += text
	})
	if !ok {
		return
	}
	t.bus.Broadcast(ctx, chatScope(chatID).event(marotte.EventToolProgress, toolProgress(turn.ID(), &before, &after)))
}
