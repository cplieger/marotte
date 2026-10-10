package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
)

// noSubscriberLine is reportNoSubscribers' message, matched whole so a rewording fails here.
const noSubscriberLine = `"msg":"no push subscribers; notifications are being dropped until a browser subscribes"`

func newDropHub(t *testing.T) (*Runtime, *recordingPush) {
	t.Helper()
	cs := newTestChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	fp.noSubs.Store(true)
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.wire(h)
	return h, fp
}

// One line per no-subscriber episode: per drop would bury the log, none would hide a dead pipeline.
func TestNotify_ReportsANoSubscriberDropOncePerEpisode(t *testing.T) {
	h, fp := newDropHub(t)
	logs := captureLogs(t)
	ctx := t.Context()

	for range 3 {
		h.coord.Notify(ctx, "c1", &dropAsk)
	}

	got := logs.String()
	if n := strings.Count(got, noSubscriberLine); n != 1 {
		t.Errorf("3 drops logged the no-subscriber line %d times, want 1; the latch is what keeps a"+
			" per-tool-call permission ask from flooding the log. Captured: %s", n, got)
	}
	if !strings.Contains(got, `"chat_id":"c1"`) {
		t.Errorf("the no-subscriber line carries no chat_id; captured: %s", got)
	}
	if !strings.Contains(got, `"kind":"`+string(marotte.PushKindPermission)+`"`) {
		t.Errorf("the no-subscriber line carries no kind; captured: %s", got)
	}
	select {
	case body := <-fp.sends:
		t.Errorf("push sent %q with no subscribers, want the notification dropped", body)
	default:
	}
}

// A subscriber arriving re-arms the latch.
func TestNotify_NoSubscriberLatchReArmsWhenASubscriberAppears(t *testing.T) {
	h, fp := newDropHub(t)
	logs := captureLogs(t)
	ctx := t.Context()

	h.coord.Notify(ctx, "c1", &dropAsk)
	if n := strings.Count(logs.String(), noSubscriberLine); n != 1 {
		t.Fatalf("first episode logged %d lines, want 1; captured: %s", n, logs.String())
	}

	fp.noSubs.Store(false)
	h.coord.Notify(ctx, "c1", &dropFinished)
	select {
	case <-fp.sends:
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent while a subscriber was present, so nothing re-armed the latch")
	}

	fp.noSubs.Store(true)
	h.coord.Notify(ctx, "c1", &dropAsk)

	got := logs.String()
	if n := strings.Count(got, noSubscriberLine); n != 2 {
		t.Errorf("after a subscriber came and went, the no-subscriber line was logged %d times in total,"+
			" want 2; the second episode is silent unless a subscriber re-arms the latch. Captured: %s", n, got)
	}
}

var (
	dropAsk      = notice.Question(notice.ChatTarget("c1", "A"), "", "Permission needed")
	dropFinished = notice.TurnFinished(notice.ChatTarget("c1", "A"), "")
)
