package server

// The readiness reason for a dead sign-in: a failed vend leaves KAS running
// unauthenticated, so readiness reports it. The prefix must NOT begin with "kiro-cli"
// (static-src/runtime-health.ts reads that prefix as an install verdict) and IS the prefix
// it matches for the sign-in family. A fixed literal: /api/health is unauthenticated.
// TestAuthReasonIsTheClientContract pins it; change runtime-health.ts in the same commit.
const reasonSignIn = "sign-in required"
