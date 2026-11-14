package marotte

// ContentCollectionSettingsParams builds MethodContentCollectionSettingsChanged's params, the same object initialize
// carries as settings.contentCollection.
func ContentCollectionSettingsParams(enabled bool) map[string]any {
	return map[string]any{"contentCollection": map[string]any{"enabled": enabled}}
}
