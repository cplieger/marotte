package marotte

import "strings"

// runHostChatPrefix namespaces the synthetic chat id a parentless run's host session registers under, which has no
// chat file. Real ids are `c-*`, so the two never collide.
const runHostChatPrefix = "run:"

// RunHostChatID is the chat id of the run host session holding workflowID.
func RunHostChatID(workflowID string) ChatID {
	return ChatID(runHostChatPrefix + workflowID)
}

// IsRunHostChat reports whether chatID is a run host session's rather than a chat's.
func IsRunHostChat(chatID ChatID) bool {
	return strings.HasPrefix(string(chatID), runHostChatPrefix)
}

// RunHostWorkflowID is the run a run host chat id names, "" for any other chat id.
func RunHostWorkflowID(chatID ChatID) string {
	id, ok := strings.CutPrefix(string(chatID), runHostChatPrefix)
	if !ok {
		return ""
	}
	return id
}
