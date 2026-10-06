// Package marotte is the app's wire and domain TYPE vocabulary: the shapes that cross the HTTP and
// SSE boundaries and the shapes its components pass each other. Besides types it holds only
// constructors and mappers over them.
// Deliberately absent: interfaces (each lives at its consumer), behaviour (internal/sanitize,
// internal/ids, internal/rpcerr and the like) and HTTP plumbing (internal/httpreply). The types
// keep the graph acyclic: marotte.ServerEvent crosses the agent/translate seam, and agent imports
// translate. cmd/wire-codegen walks them through internal/wirespec. This package imports no other
// marotte-internal package.
package marotte
