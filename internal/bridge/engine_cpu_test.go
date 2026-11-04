package bridge

import (
	"sync/atomic"
	"testing"
)

// advancingCPU is a CPU probe whose reading grows on every call: an engine that keeps working.
func advancingCPU() func() (uint64, bool) {
	var ticks atomic.Uint64
	return func() (uint64, bool) { return ticks.Add(1), true }
}

func TestParseStatCPU(t *testing.T) {
	// Fields after comm: state(3) ppid pgrp session tty_nr tpgid flags minflt cminflt majflt cmajflt utime(14) stime(15).
	const tail = " S 1 2 3 4 5 6 7 8 9 10 1234 56 0 0 20 0 11 0 100 2000 300"
	cases := map[string]struct {
		raw    string
		want   uint64
		wantOK bool
	}{
		"plain comm":                  {raw: "159 (node)" + tail, want: 1290, wantOK: true},
		"comm with spaces and parens": {raw: "159 (MainThread) (x y)" + tail, want: 1290, wantOK: true},
		"trailing newline":            {raw: "159 (node)" + tail + "\n", want: 1290, wantOK: true},
		"no closing paren":            {raw: "159 node" + tail},
		"line cut before stime":       {raw: "159 (node) S 1 2 3 4 5 6 7 8 9 10 1234"},
		"non-numeric utime":           {raw: "159 (node) S 1 2 3 4 5 6 7 8 9 10 x 56 0"},
		"non-numeric stime":           {raw: "159 (node) S 1 2 3 4 5 6 7 8 9 10 1234 - 0"},
		"empty":                       {raw: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := parseStatCPU([]byte(tc.raw))
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parseStatCPU(%q) = (%d, %v), want (%d, %v)", tc.raw, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
