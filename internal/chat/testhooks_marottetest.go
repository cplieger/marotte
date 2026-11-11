//go:build marotte_test

package chat

import "time"

// The e2e-sse suite counts header frames per prompt; a window no runner's fsync stall reaches
// keeps one open's saves in one frame there.
func init() { headerPublishWindow = time.Second }
