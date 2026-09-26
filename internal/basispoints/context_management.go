package basispoints

import "maps"

// This is a plugin fallback policy, not an upstream/model context limit. It is
// used only when the client explicitly requests compaction without a threshold.
const DefaultCompactThreshold = 200_000

func normalizeContextManagement(policy any, threshold int) any {
	entries, ok := policy.([]any)
	if !ok {
		return policy
	}
	normalized := append([]any(nil), entries...)
	for i, value := range entries {
		entry := objectValue(value)
		if entry == nil || stringValue(entry["type"]) != "compaction" {
			continue
		}
		if existing, present := entry["compact_threshold"]; present && existing != nil {
			continue
		}
		copy := maps.Clone(entry)
		copy["compact_threshold"] = threshold
		normalized[i] = copy
	}
	return normalized
}
