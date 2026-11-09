package apitable

import "github.com/cplieger/marotte/internal/route"

// GateCodes is the codes the gate can answer an entry with, derived from its Access alone. It is
// empty until the gate is wired over this table.
func GateCodes(*route.Entry) route.CodeSet { return nil }
