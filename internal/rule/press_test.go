package rule

import (
	"strconv"
	"strings"
	"testing"
)

func TestRecordPress_KeepsTheLastTenPressesAndCutsTheirReasons(t *testing.T) {
	var st Status
	for i := range 11 {
		RecordPress(&st, Press{EventID: "manual:op-" + strconv.Itoa(i), Outcome: PressQueued})
	}
	if _, ok := PressOf(&st, "manual:op-0"); ok {
		t.Errorf("the oldest of 11 presses is still stored, want the last 10 kept")
	}
	if p, ok := PressOf(&st, "manual:op-10"); !ok || p.Outcome != PressQueued {
		t.Errorf("PressOf(op-10) = %+v, %v, want the newest press queued", p, ok)
	}
	got := RecordPress(&st, Press{EventID: "manual:long", Outcome: PressFailed, Reason: strings.Repeat("x", 2000)})
	if len(got.Reason) > 1024 || !strings.HasSuffix(got.Reason, "…") {
		t.Errorf("reason of %d bytes, want it cut to 1 KiB with an ellipsis", len(got.Reason))
	}
	if p, _ := PressOf(&st, "manual:long"); p != got {
		t.Errorf("stored %+v, want what RecordPress answered %+v", p, got)
	}
}
