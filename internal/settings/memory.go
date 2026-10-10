package settings

import (
	"encoding/json"
	"fmt"

	"github.com/cplieger/marotte/internal/marotte"
)

// The four values of KeyMemoryMode, one per option of the Memory dropdown.
const (
	MemoryOff       = "off"
	memoryReadOnly  = "read_only"
	memoryReadWrite = "read_write"
	memoryLearn     = "learn"
)

// DefaultMemoryMode is kiro-cli's shipped default: read and write with
// background learning (KAS's own fallback is {read_write, reflection: true}).
const DefaultMemoryMode = memoryLearn

// validMemoryMode reports whether mode is one of the four dropdown values.
func validMemoryMode(mode string) bool {
	switch mode {
	case MemoryOff, memoryReadOnly, memoryReadWrite, memoryLearn:
		return true
	}
	return false
}

// MemoryPreferenceFor maps a dropdown value onto KAS's `settings.memory`
// fields. An unrecognised value takes DefaultMemoryMode.
func MemoryPreferenceFor(mode string) marotte.MemoryPreference {
	switch mode {
	case MemoryOff:
		return marotte.MemoryPreference{Mode: "disabled"}
	case memoryReadOnly:
		return marotte.MemoryPreference{Mode: "read_only"}
	case memoryReadWrite:
		return marotte.MemoryPreference{Mode: "read_write"}
	default: // memoryLearn, and DefaultMemoryMode for anything unrecognised
		return marotte.MemoryPreference{Mode: "read_write", Reflection: true}
	}
}

// decodeMemoryMode is decodeInto plus the value check: the spawn path reads the
// same key, so a well-typed value outside the four keeps the default here too.
func decodeMemoryMode(dst *string, raw json.RawMessage) error {
	var mode string
	if err := decodeInto(&mode, raw); err != nil {
		return err
	}
	if !validMemoryMode(mode) {
		return fmt.Errorf("settings: %s %q is not one of off, read_only, read_write, learn", KeyMemoryMode, mode)
	}
	*dst = mode
	return nil
}
