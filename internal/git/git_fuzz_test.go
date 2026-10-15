package git

import (
	"testing"
)

func FuzzStatusLabel(f *testing.F) {
	f.Add(byte('M'))
	f.Add(byte('A'))
	f.Add(byte('D'))
	f.Add(byte('R'))
	f.Add(byte('C'))
	f.Add(byte('U'))
	f.Add(byte('?'))
	f.Add(byte('X'))
	f.Add(byte(0))

	f.Fuzz(func(t *testing.T, c byte) {
		result := statusLabel(c)
		if result == "" {
			t.Fatal("statusLabel returned empty string")
		}
	})
}
