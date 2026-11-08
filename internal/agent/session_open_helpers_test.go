package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// openReleased is openBridge for a test that needs the entry and not the caller's open hold on it: the hold ends
// before it returns, so a close the test drives later is not held by the test's own open.
func (bc *bridgeCoordinator) openReleased(ctx context.Context, chatID marotte.ChatID, model string) (*sharedBridge, error) {
	sb, release, err := bc.openBridge(ctx, chatID, model)
	release()
	return sb, err
}

// openAtReleased is openReleased for the holder of res (openBridgeAt).
func (bc *bridgeCoordinator) openAtReleased(
	ctx context.Context, chatID marotte.ChatID, model string, res command.Reservation,
) (*sharedBridge, error) {
	sb, release, err := bc.openBridgeAt(ctx, chatID, model, res)
	release()
	return sb, err
}

// residentReleased is ensureResident with its open hold ended before it returns (openReleased).
func (rs *Runs) residentReleased(ctx context.Context, workflowID string) (*drivenHost, error) {
	host, release, err := rs.ensureResident(ctx, workflowID)
	release()
	return host, err
}
