package agent

import "testing"

func TestReadRun_EndsTheReadAtOnceWhenNoReadStarts(t *testing.T) {
	draining := &lifetime{shutdownCtx: t.Context()}
	draining.draining.Store(true)
	for _, tc := range []struct {
		name string
		rs   *Runs
	}{
		{name: "no lifecycle", rs: &Runs{}},
		{name: "a draining lifecycle", rs: &Runs{lifecycle: draining}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := 0
			tc.rs.ReadRun("wf_5e1f0c2a9b7d4e36", func() { done++ })
			if done != 1 {
				t.Errorf("done ran %d times, want once: the read never started", done)
			}
		})
	}
}
