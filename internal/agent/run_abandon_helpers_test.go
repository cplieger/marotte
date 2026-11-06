package agent

// ownerUsers is how many callers own or wait for the run's owner.
func ownerUsers(rs *Runs, workflowID string) int {
	rs.owners.mu.Lock()
	defer rs.owners.mu.Unlock()
	if t := rs.owners.turns[workflowID]; t != nil {
		return t.users
	}
	return 0
}
