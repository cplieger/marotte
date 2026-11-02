package translate

import (
	"slices"
	"testing"
	"unicode/utf8"
)

// FuzzHandleAssistantChunk_SteerAcksIgnoreChunking is the chunking oracle at any number of cuts:
// a reply cut by cuts (each byte one chunk's length), each chunk carrying the messageId where its
// bit in ids is set, files the prose and acks of the reply sent whole, live and in the replay
// projection, with the acknowledged steers queued or not. A steer read between chunks where its bit
// in interject is set changes neither the prose nor the entry ids. The seeds are the chunkings
// recorded in live chats and found by reviews.
func FuzzHandleAssistantChunk_SteerAcksIgnoreChunking(f *testing.F) {
	const (
		all    = ^uint64(0)
		none   = uint64(0)
		middle = uint64(0b101) // the second of three chunks id-less
	)
	seeds := []struct {
		text      string
		cuts      []byte
		ids       uint64
		opening   bool
		queued    bool
		interject uint64
	}{
		// `…decide.\n\n[STEER` | `ING A…]\n\n[STEER` | `ING B…]`, the first recorded chat.
		{steerChunkingSamples[0].text, []byte{15, 28}, all, true, false, 0},
		{steerChunkingSamples[0].text, []byte{15, 28}, all, true, true, 0},
		{steerChunkingSamples[0].text, []byte{15, 28}, none, false, false, 0},
		// The same cut one past the 16-byte prefix: `[STEERING steer-B` is withheld whole.
		{steerChunkingSamples[0].text, []byte{9, 45}, all, true, false, 0},
		// Three markers, each closing chunk opening the next, the second recorded chat.
		{steerChunkingSamples[1].text, []byte{12, 23, 23}, all, true, false, 0},
		{steerChunkingSamples[1].text, []byte{12, 23, 23}, middle, false, true, 0},
		// `[A]` | id-less `x [ST` | `EERING B…]`: prose after an ack, then a held prefix.
		{steerChunkingSamples[3].text, []byte{34, 5}, middle, true, false, 0},
		{steerChunkingSamples[3].text, []byte{34, 5}, middle, true, true, 0},
		// A reply that opens with `[STEER` | `ING A…]\n\n[STEER` | the rest.
		{steerChunkingSamples[2].text, []byte{6, 28}, none, false, false, 0},
		{steerChunkingSamples[2].text, []byte{6, 28}, middle, false, true, 0},
		{steerChunkingSamples[4].text, []byte{6, 7, 9, 12}, middle, true, false, 0},
		// A steer read mid-word, and one inside a withheld `[STEER` prefix.
		{"I need to locate the arrapi agent on Agent Mail.", []byte{39}, all, false, false, 0b1},
		{steerChunkingSamples[0].text, []byte{15, 28}, all, true, true, 0b11},
	}
	for _, s := range seeds {
		f.Add(s.text, s.cuts, s.ids, s.opening, s.queued, s.interject)
	}
	f.Fuzz(func(t *testing.T, text string, cuts []byte, ids uint64, opening, queued bool, interject uint64) {
		// JSON frames hold whole code points, and a marker longer than maxSteerCarry is released
		// as prose by design, so neither is a chunking the oracle can hold.
		if text == "" || len(text) > 1024 || !utf8.ValidString(text) {
			t.Skip()
		}
		chunks := cutSteerChunks(text, cuts, ids)
		for _, c := range chunks {
			if isInterruptSentinel(c.text) {
				t.Skip()
			}
		}
		var lead []steerChunk
		if opening {
			lead = []steerChunk{{text: "Opening. ", id: true}}
		}
		want := liveSteerRecord(t, append(slices.Clone(lead), steerChunk{text: text, id: true}), queued)
		chunks = append(lead, chunks...)
		if got := liveSteerRecord(t, chunks, queued); !slices.Equal(got, want) {
			t.Fatalf("chunks %s:\n got  %q\n want %q", describeChunks(chunks), got, want)
		}
		if got := projectedProse(t, chunks); got != proseOf(want) {
			t.Fatalf("projected chunks %s:\n got  %q\n want %q", describeChunks(chunks), got, proseOf(want))
		}
		_, plain := liveInterjectedRecord(t, chunks, queued, 0)
		spans, interjected := liveInterjectedRecord(t, chunks, queued, interject)
		if !slices.Equal(spans, want) || !slices.Equal(interjected, plain) {
			t.Fatalf("chunks %s with steers read after %b:\n got  %q %q\n want %q %q",
				describeChunks(chunks), interject, spans, interjected, want, plain)
		}
	})
}

// cutSteerChunks cuts text into chunks of the lengths in cuts (a zero is one byte), moved forward
// to a rune boundary; the rest is the last chunk. Chunk k carries the messageId when bit k%64 of
// ids is set.
func cutSteerChunks(text string, cuts []byte, ids uint64) []steerChunk {
	var out []steerChunk
	add := func(s string) {
		out = append(out, steerChunk{text: s, id: ids&(1<<(len(out)%64)) != 0})
	}
	for _, n := range cuts {
		if text == "" {
			break
		}
		at := min(max(int(n), 1), len(text))
		for at < len(text) && !utf8.RuneStart(text[at]) {
			at++
		}
		add(text[:at])
		text = text[at:]
	}
	if text != "" {
		add(text)
	}
	return out
}
