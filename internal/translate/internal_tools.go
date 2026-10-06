package translate

// isInternalTool reports engine bookkeeping KAS announces as tool calls (its TUI never renders
// them). The create frame is dropped before it can open a turn.
func isInternalTool(toolID string) bool {
	return toolID == "fetch_cloud_config"
}
