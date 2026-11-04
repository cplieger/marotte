package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestChatGates_AStampBeforeACloseIsRefusedAfterIt(t *testing.T) {
	g := newChatGates()
	before := g.stamp()
	h := g.hold("c1")
	h.commit()
	h.release()
	if v, _ := g.admit("c1", before); v != gateClosedSince {
		t.Errorf("a stamp from before the close: verdict %v, want closedSince", v)
	}
	if v, _ := g.admit("c1", g.stamp()); v != gateAdmitted {
		t.Errorf("a stamp from after the close's release: verdict %v, want admitted", v)
	}
	if v, _ := g.admit("c2", before); v != gateAdmitted {
		t.Errorf("another chat: verdict %v, want admitted", v)
	}
}

func TestChatGates_AStampTakenDuringAHoldWaitsAndIsAdmittedAfterTheRelease(t *testing.T) {
	g := newChatGates()
	h := g.hold("c1")
	s := g.stamp()
	v, idle := g.admit("c1", s)
	if v != gateWait || idle == nil {
		t.Fatalf("during the hold: verdict %v idle %v, want wait with a channel", v, idle)
	}
	h.release()
	select {
	case <-idle:
	case <-time.After(time.Second):
		t.Fatal("the release did not close the idle channel")
	}
	if v, _ := g.admit("c1", s); v != gateAdmitted {
		t.Errorf("after an uncommitted release: verdict %v, want admitted", v)
	}
}

func TestChatGates_TwoHoldsReleaseOnlyAtTheSecond(t *testing.T) {
	g := newChatGates()
	a, b := g.hold("c1"), g.hold("c1")
	a.release()
	a.release()
	if v, _ := g.admit("c1", g.stamp()); v != gateWait {
		t.Errorf("one of two holds released: verdict %v, want wait", v)
	}
	b.release()
	if v, _ := g.admit("c1", g.stamp()); v != gateAdmitted {
		t.Errorf("both released: verdict %v, want admitted", v)
	}
}

func TestChatGates_AnUncommittedReleaseLeavesLastUnchanged(t *testing.T) {
	g := newChatGates()
	s := g.stamp()
	h := g.hold("c1")
	h.release()
	if v, _ := g.admit("c1", s); v != gateAdmitted {
		t.Errorf("a refused close ended a stamp: verdict %v, want admitted", v)
	}
}

func TestChatGates_AStampFromBeforeADeleteIsRefusedAfterItsJob(t *testing.T) {
	g := newChatGates()
	old := g.stamp()
	h := g.hold("c1")
	_, idle := g.admit("c1", old)
	woke := make(chan gateVerdict, 1)
	go func() {
		<-idle
		v, _ := g.admit("c1", old)
		woke <- v
	}()
	h.commit()
	h.release()
	select {
	case v := <-woke:
		if v != gateClosedSince {
			t.Errorf("the woken waiter: verdict %v, want closedSince", v)
		}
	case <-time.After(time.Second):
		t.Fatal("the waiter was not woken")
	}
	g.mu.Lock()
	_, kept := g.chats["c1"]
	g.mu.Unlock()
	if !kept {
		t.Error("the gate entry was dropped after its job")
	}
}

func TestChatGates_AwaitNoCloseWaitsForTheHold(t *testing.T) {
	g := newChatGates()
	t.Run("no hold", func(t *testing.T) {
		waited, err := g.awaitNoClose(t.Context(), "c1")
		if waited || err != nil {
			t.Errorf("waited %v err %v, want false nil", waited, err)
		}
	})
	t.Run("a standing hold", func(t *testing.T) {
		h := g.hold("c1")
		go func() {
			time.Sleep(20 * time.Millisecond)
			h.release()
		}()
		waited, err := g.awaitNoClose(t.Context(), "c1")
		if !waited || err != nil {
			t.Errorf("waited %v err %v, want true nil", waited, err)
		}
	})
	t.Run("ctx ends first", func(t *testing.T) {
		h := g.hold("c1")
		defer h.release()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		_, err := g.awaitNoClose(ctx, "c1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err %v, want the ctx error", err)
		}
	})
}
