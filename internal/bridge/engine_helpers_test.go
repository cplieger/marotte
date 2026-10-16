package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// fakeKAS is the test standing in for kiro-cli on the other end of a live engine's pipes: it reads what the engine
// writes and writes what KAS would send, with the engine's real read loop, lanes and write path in between.
type fakeKAS struct {
	t      *testing.T
	e      *Engine
	in     *bufio.Reader
	inR    *os.File
	outW   *os.File
	frames chan wireFrame
	mu     sync.Mutex
}

// wireFrame is one frame the engine wrote.
type wireFrame struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	raw    string
}

func (f *wireFrame) param(key string) string {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(f.Params, &m)
	var s string
	_ = json.Unmarshal(m[key], &s)
	return s
}

// overrideID is the `_meta.kiro.overrideSessionId` a session/new carried.
func (f *wireFrame) overrideID() marotte.SessionID {
	var p struct {
		Meta struct {
			Kiro struct {
				Override marotte.SessionID `json:"overrideSessionId"`
			} `json:"kiro"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(f.Params, &p)
	return p.Meta.Kiro.Override
}

func newFakeKAS(t *testing.T, opts ...Option) *fakeKAS {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdout pipe: %v", err)
	}
	e := NewEngine("/unused", "/work", opts...)
	e.lifecycleCtx = context.Background()
	e.stdin.Store(&stdinPipe{w: inW})
	e.stdout = newFrameReader(bufio.NewReaderSize(outR, stdoutBufSize))
	e.readLoopStarted.Store(true)
	k := &fakeKAS{t: t, e: e, in: bufio.NewReader(inR), inR: inR, outW: outW, frames: make(chan wireFrame, 4096)}
	go e.readLoop()
	// What the reap does to a live process: its stdout ends once marotte tears the engine down.
	go func() {
		<-e.stopping
		_ = outW.Close()
	}()
	go k.readAll()
	t.Cleanup(func() {
		e.Stop()
		_ = inR.Close()
	})
	return k
}

func (k *fakeKAS) readAll() {
	for {
		line, err := k.in.ReadBytes('\n')
		if err != nil {
			close(k.frames)
			return
		}
		var f wireFrame
		if json.Unmarshal(line, &f) != nil {
			continue
		}
		f.raw = string(line)
		k.frames <- f
	}
}

// next returns the next frame the engine wrote, failing after 2 s.
func (k *fakeKAS) next() wireFrame {
	k.t.Helper()
	select {
	case f, ok := <-k.frames:
		if !ok {
			k.t.Fatal("the engine closed its stdin before the frame the test waited for")
		}
		return f
	case <-time.After(2 * time.Second):
		k.t.Fatal("the engine wrote no frame within 2s")
	}
	return wireFrame{}
}

// nextMethod skips frames until one with method, failing after 2 s.
func (k *fakeKAS) nextMethod(method string) wireFrame {
	k.t.Helper()
	for {
		if f := k.next(); f.Method == method {
			return f
		}
	}
}

// quiet reports that the engine wrote nothing within d.
func (k *fakeKAS) quiet(d time.Duration) (wireFrame, bool) {
	select {
	case f, ok := <-k.frames:
		return f, !ok
	case <-time.After(d):
		return wireFrame{}, true
	}
}

func (k *fakeKAS) send(v any) {
	k.t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		k.t.Fatalf("marshal frame: %v", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, err := k.outW.Write(append(raw, '\n')); err != nil {
		k.t.Fatalf("write frame to the engine: %v", err)
	}
}

func (k *fakeKAS) reply(id *int64, result any) {
	k.t.Helper()
	k.send(map[string]any{"jsonrpc": "2.0", "id": *id, "result": result})
}

func (k *fakeKAS) replyError(id *int64, code int, msg string) {
	k.t.Helper()
	k.send(map[string]any{"jsonrpc": "2.0", "id": *id, "error": map[string]any{"code": code, "message": msg}})
}

func (k *fakeKAS) notify(method string, params any) {
	k.t.Helper()
	k.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (k *fakeKAS) request(id int64, method string, params any) {
	k.t.Helper()
	k.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

// openedSession runs a NewSession the fake answers with the minted id, answering every later door call too.
func (k *fakeKAS) openedSession(attach func(*Session)) *Session {
	k.t.Helper()
	type result struct {
		s   *Session
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := k.e.NewSession(context.Background(), &marotte.SessionOpts{Role: marotte.SessionRoleChat}, attach)
		done <- result{s, err}
	}()
	f := k.nextMethod(methodSessionNew)
	k.reply(f.ID, map[string]any{"sessionId": f.overrideID()})
	for {
		select {
		case r := <-done:
			if r.err != nil {
				k.t.Fatalf("NewSession: %v", r.err)
			}
			return r.s
		case f := <-k.frames:
			if f.ID != nil && f.Method != "" {
				k.reply(f.ID, map[string]any{})
			}
		case <-time.After(2 * time.Second):
			k.t.Fatal("NewSession did not return")
		}
	}
}

// drain reads a lane until it closes or d passes, returning what it read.
func drain(ch <-chan marotte.Notification, d time.Duration) []marotte.Notification {
	var got []marotte.Notification
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case n, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, n)
		case <-timer.C:
			return got
		}
	}
}

// answerUntilErr answers every request the engine writes until the call reporting on done returns, failing on its error.
func (k *fakeKAS) answerUntilErr(done <-chan error) {
	k.t.Helper()
	for {
		select {
		case err := <-done:
			if err != nil {
				k.t.Fatalf("the call the test waited for: %v", err)
			}
			return
		case f := <-k.frames:
			if f.ID != nil && f.Method != "" {
				k.reply(f.ID, map[string]any{})
			}
		case <-time.After(2 * time.Second):
			k.t.Fatal("the call the test waited for never returned")
		}
	}
}
