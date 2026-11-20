package marotte

// BackgroundJobState is a kiro-cli background job's state: running, then one outcome. marotte maps a value
// outside the set to outcome_unknown, so the set stays closed for the client's validators.
type BackgroundJobState string

// BackgroundJobRunning and the following constants are the valid BackgroundJobState values.
const (
	BackgroundJobRunning        BackgroundJobState = "running"
	BackgroundJobCompleted      BackgroundJobState = "completed"
	BackgroundJobFailed         BackgroundJobState = "failed"
	BackgroundJobCancelled      BackgroundJobState = "cancelled"
	BackgroundJobTimedOut       BackgroundJobState = "timed_out"
	BackgroundJobOutcomeUnknown BackgroundJobState = "outcome_unknown"
)

// BackgroundJob marks a tool call kiro-cli moved into a background job (_meta.kiro.job). End is nil while
// the job runs.
type BackgroundJob struct {
	End *BackgroundJobEnd `json:"end,omitempty"`
	ID  string            `json:"id"`
}

// BackgroundJobEnd is how a job ended (_meta.kiro.job.end, or a job notice's jobEnd). Status is never
// running. StopReason is kiro-cli's model, user, session_close or shutdown, kept as sent; the times are
// RFC 3339.
type BackgroundJobEnd struct {
	Status     BackgroundJobState `json:"status"`
	StopReason string             `json:"stop_reason,omitempty"`
	StartedAt  string             `json:"started_at,omitempty"`
	EndedAt    string             `json:"ended_at,omitempty"`
	DurationMs int64              `json:"duration_ms"`
}

// EntryJobEnded is a background job's end, filed once per job (JobEndedID) from the card's end update or
// kiro-cli's job notice, whichever arrives first. ToolCallID names the card when the end came from it,
// and Output is the tail of the job's result.
type EntryJobEnded struct {
	JobID       string           `json:"job_id"`
	ToolCallID  string           `json:"tool_call_id,omitempty"`
	Description string           `json:"description,omitempty"`
	Output      string           `json:"output,omitempty"`
	End         BackgroundJobEnd `json:"end"`
}

// JobEndedID is the id of a job's job_ended entry.
func JobEndedID(jobID string) string { return "job:" + jobID + ":ended" }

// BackgroundJobPayload is the payload for type="job_update" (_kiro/job/update) and one row of GET
// /api/jobs. Kind is kiro-cli's job kind (shell), kept as sent. ChatID is set on a GET row only: an
// event names its chat on the envelope.
type BackgroundJobPayload struct {
	ChatID      ChatID             `json:"chat_id,omitempty"`
	ID          string             `json:"id"`
	Kind        string             `json:"kind,omitempty"`
	Description string             `json:"description"`
	Status      BackgroundJobState `json:"status"`
	StartedAt   string             `json:"started_at,omitempty"`
	EndedAt     string             `json:"ended_at,omitempty"`
}

// BackgroundJobsView is GET /api/jobs: every chat's running jobs, by chat then oldest first.
type BackgroundJobsView struct {
	Jobs []BackgroundJobPayload `json:"jobs"`
}

// BackgroundJobOutput is GET /api/chats/{id}/jobs/{job}/output: the last lines of the job's log. Gap is set
// when kiro-cli skipped lines it could not read whole.
type BackgroundJobOutput struct {
	Status BackgroundJobState `json:"status"`
	Lines  []string           `json:"lines"`
	Gap    bool               `json:"gap,omitempty"`
}
