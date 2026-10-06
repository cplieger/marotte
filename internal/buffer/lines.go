// Package buffer tracks which lines of a file the agent changed and the line delta of one diff. The per-turn content
// accumulator is internal/turnlog.
package buffer

import (
	"container/heap"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// DefaultOutputCap is the shared byte budget for subprocess output buffers: a 200×50 screen with heavy ANSI.
const DefaultOutputCap = 64 * 1024

// LineRange is a range of lines modified by the agent.
type LineRange struct {
	Kind      string `json:"kind"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Turn      int    `json:"turn"`
}

// maxLineRangesPerFile caps per-file ranges.
const maxLineRangesPerFile = 200

// maxFilesPerChat caps the number of distinct file paths tracked per chat.
const maxFilesPerChat = 500

// fileHeapEntry tracks a file's last turn for heap-based eviction.
type fileHeapEntry struct {
	path     string
	lastTurn int
	index    int // heap index
}

// fileHeap implements heap.Interface for O(log n) eviction of the oldest file.
type fileHeap []*fileHeapEntry

func (h fileHeap) Len() int           { return len(h) }
func (h fileHeap) Less(i, j int) bool { return h[i].lastTurn < h[j].lastTurn }
func (h fileHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *fileHeap) Push(x any)        { e, _ := x.(*fileHeapEntry); e.index = len(*h); *h = append(*h, e) }

func (h *fileHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	e.index = -1
	*h = old[:n-1]
	return e
}

// chatLineState holds per-chat line tracking data with a heap for eviction.
type chatLineState struct {
	ranges  map[string][]LineRange
	entries map[string]*fileHeapEntry
	h       fileHeap
}

// LineTracker tracks per-file line changes across all chats.
type LineTracker struct {
	data map[marotte.ChatID]*chatLineState
	mu   sync.RWMutex
}

// NewLineTracker creates a new LineTracker.
func NewLineTracker() *LineTracker {
	return &LineTracker{data: make(map[marotte.ChatID]*chatLineState)}
}

// Record adds a line range for a file change. A LineRange, not three adjacent ints: a silent transposition would
// invert the gutter range or corrupt the eviction key, since the heap orders files by lastTurn.
func (lt *LineTracker) Record(chatID marotte.ChatID, filePath string, r LineRange) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	state := lt.data[chatID]
	if state == nil {
		state = &chatLineState{
			ranges:  make(map[string][]LineRange),
			entries: make(map[string]*fileHeapEntry),
		}
		lt.data[chatID] = state
	}
	if _, exists := state.ranges[filePath]; !exists && len(state.ranges) >= maxFilesPerChat {
		e, _ := heap.Pop(&state.h).(*fileHeapEntry)
		delete(state.ranges, e.path)
		delete(state.entries, e.path)
	}
	existing := state.ranges[filePath]
	if len(existing) >= maxLineRangesPerFile {
		existing = existing[1:]
	}
	state.ranges[filePath] = append(existing, r)
	if e, ok := state.entries[filePath]; ok {
		e.lastTurn = r.Turn
		heap.Fix(&state.h, e.index)
	} else {
		e = &fileHeapEntry{path: filePath, lastTurn: r.Turn}
		state.entries[filePath] = e
		heap.Push(&state.h, e)
	}
}

// RecordFromDiffs records one range per diff hunk, in new-text line numbers, so KAS's whole-file NewText does not
// mark every line. A whole-file rewrite yields one full span.
func (lt *LineTracker) RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string) {
	for _, d := range diffs {
		if d.Path == "" || d.NewText == "" {
			continue
		}
		for _, h := range lineHunks(d.OldText, d.NewText) {
			lt.Record(chatID, d.Path, LineRange{StartLine: h.StartLine, EndLine: h.EndLine, Turn: turn, Kind: kind})
		}
	}
}

// Get returns a copy of the line ranges for a file in a chat: the HTTP handler reads after the lock is released
// while Record keeps appending to the same key.
func (lt *LineTracker) Get(chatID marotte.ChatID, filePath string) []LineRange {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	state := lt.data[chatID]
	if state == nil {
		return nil
	}
	return slices.Clone(state.ranges[filePath])
}

// Clear removes all tracking data for a chat.
func (lt *LineTracker) Clear(chatID marotte.ChatID) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	delete(lt.data, chatID)
}
