package basispoints

import "fmt"

// Only fixed field names, counts and booleans are emitted; never client values.
func requestShape(body map[string]any) string {
	counts := map[string]int{}
	items, _ := body["input"].([]any)
	maxText, encrypted, badContent := 0, 0, 0
	for _, v := range items {
		item := objectValue(v)
		kind := stringValue(item["type"])
		switch kind {
		case "message", "reasoning", "compaction", "compaction_trigger", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output", "web_search_call":
		case "":
			kind = "message"
		default:
			kind = "other"
		}
		counts[kind]++
		if stringValue(item["encrypted_content"]) != "" {
			encrypted++
		}
		if s, ok := item["content"].(string); ok && len(s) > maxText {
			maxText = len(s)
		}
		parts, _ := item["content"].([]any)
		for _, p := range parts {
			part := objectValue(p)
			k := stringValue(part["type"])
			if k != "input_text" && k != "output_text" && k != "input_image" && k != "refusal" {
				badContent++
			}
			if n := len(stringValue(part["text"])); n > maxText {
				maxText = n
			}
		}
	}
	policy, present := body["context_management"]
	policies, array := policy.([]any)
	threshold := 0
	for _, v := range policies {
		entry := objectValue(v)
		if stringValue(entry["type"]) == "compaction" {
			switch v := entry["compact_threshold"].(type) {
			case int:
				threshold = v
			case float64:
				threshold = int(v)
			default:
				threshold = -1
			}
		}
	}
	return fmt.Sprintf("shape=%s; input_items=%d; encrypted_items=%d; max_text_bytes=%d; other_content=%d; policy_present=%t; policy_array=%t; policies=%d; threshold=%d", jsonBytes(counts), len(items), encrypted, maxText, badContent, present, array, len(policies), threshold)
}
