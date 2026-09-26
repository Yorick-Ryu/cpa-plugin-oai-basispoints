package basispoints

// stripCacheCreationUsage exposes normal Responses usage to downstream clients.
// Keep input/output totals and cache-read counts exactly as reported upstream;
// only the separate cache-creation breakdown is omitted. This shared boundary
// runs before JSON, Codex terminal events, and synthetic SSE are serialized.
func stripCacheCreationUsage(response map[string]any) {
	usage, ok := response["usage"].(map[string]any)
	if !ok {
		return
	}
	strip := func(details map[string]any) {
		for _, key := range []string{
			"cache_write_tokens", "cache_creation_tokens", "cached_creation_tokens",
			"cache_creation_input_tokens", "cache_creation", "cache_creation_tokens_5m",
			"cache_creation_tokens_1h", "cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens",
		} {
			delete(details, key)
		}
	}
	strip(usage)
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		if details, ok := usage[key].(map[string]any); ok {
			strip(details)
		}
	}
}
