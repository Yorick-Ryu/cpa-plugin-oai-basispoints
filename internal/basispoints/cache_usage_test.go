package basispoints

import (
	"reflect"
	"testing"
)

func TestStripCacheCreationPreservesOrdinaryUsage(t *testing.T) {
	expected := map[string]any{
		"input_tokens": 100, "output_tokens": 20, "total_tokens": 120,
		"input_tokens_details":  map[string]any{"cached_tokens": 40, "image_tokens": 8},
		"prompt_tokens_details": map[string]any{"cached_tokens": 40, "audio_tokens": 2},
		"output_tokens_details": map[string]any{"reasoning_tokens": 10},
		"custom_usage":          "preserved",
	}
	usage := cloneObject(expected)
	for _, container := range []map[string]any{usage, objectValue(usage["input_tokens_details"]), objectValue(usage["prompt_tokens_details"])} {
		for _, key := range []string{"cache_write_tokens", "cache_creation_tokens", "cached_creation_tokens", "cache_creation_input_tokens", "cache_creation_tokens_5m", "cache_creation_tokens_1h", "cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens"} {
			container[key] = 30
		}
		container["cache_creation"] = map[string]any{"ephemeral_5m_input_tokens": 30}
	}
	response := map[string]any{"usage": usage, "status": "incomplete", "output": []any{map[string]any{"text": "cache_write_tokens"}}, "metadata": map[string]any{"cache_write_tokens": 123}}
	stripCacheCreationUsage(response)
	if !reflect.DeepEqual(jsonBytes(usage), jsonBytes(expected)) {
		t.Fatalf("usage changed unexpectedly: %s", jsonBytes(usage))
	}
	if response["status"] != "incomplete" || objectValue(response["metadata"])["cache_write_tokens"] != 123 {
		t.Fatal("unrelated response changed")
	}
	stripCacheCreationUsage(response)
	if !reflect.DeepEqual(jsonBytes(usage), jsonBytes(expected)) {
		t.Fatal("filter is not idempotent")
	}
}

func TestStripCacheCreationMissingUsage(t *testing.T) {
	for _, response := range []map[string]any{nil, {}, {"usage": nil}, {"usage": "unknown"}, {"usage": map[string]any{"input_tokens_details": nil}}} {
		before := string(jsonBytes(response))
		stripCacheCreationUsage(response)
		if string(jsonBytes(response)) != before {
			t.Fatal("missing or unknown usage changed")
		}
	}
}
