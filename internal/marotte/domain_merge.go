package marotte

// TangentMerge is one merge_tangent operation, recorded on the tangent's chat under its op_id.
type TangentMerge struct {
	OpID   string `json:"op_id"`
	Parent ChatID `json:"parent_chat_id"`
	// DeliveryID is the message id the summary reaches the parent under, as a prompt or a queued
	// row: what tells a later process whether a merge it never saw settle delivered.
	DeliveryID string            `json:"delivery_id"`
	State      TangentMergeState `json:"state"`
	// Message is a failed merge's reason, the text of its tangent_merge_failed frame.
	Message string `json:"message,omitempty"`
	// SettledAt is when the merge reached a terminal state, in unix milliseconds.
	SettledAt int64 `json:"settled_at,omitempty"`
}

// TangentMergeState is where a recorded merge stands.
type TangentMergeState string

const (
	// TangentMergeRunning is an admitted merge whose summary or delivery has not finished.
	TangentMergeRunning TangentMergeState = "running"
	// TangentMergeSucceeded is a merge whose summary reached the parent.
	TangentMergeSucceeded TangentMergeState = "succeeded"
	// TangentMergeFailed is a merge that ended without reaching the parent.
	TangentMergeFailed TangentMergeState = "failed"
)
