package basispoints

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

func TestIgnoreFastTierScopedToPreparedBasisPointsRequest(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, format := range []string{"openai-response", "codex"} {
				for _, original := range []bool{false, true} {
					for _, tier := range []any{nil, "auto", "default", "priority", "fast", "flex", "", map[string]any{"invalid": true}} {
						t.Run(fmt.Sprintf("enabled=%t/stream=%t/%s/original=%t/tier=%v", enabled, stream, format, original, tier), func(t *testing.T) {
							s := NewService()
							s.cfg.IgnoreFastTier = enabled
							raw := jsonBytes(map[string]any{"model": DefaultModelID, "input": "Hello", "service_tier": tier, "reasoning": map[string]any{"effort": "high"}})
							req := ExecutorRequest{Model: DefaultModelID, Payload: bytes.Clone(raw), SourceFormat: format, Stream: stream, StorageJSON: jsonBytes(map[string]any{"access_token": "fixture", "account_id": "fixture"})}
							if original {
								req.OriginalRequest = bytes.Clone(raw)
							}
							body, _, err := s.prepareRequest(req)
							valid := tier == nil || tier == "auto" || tier == "default" || (enabled && (tier == "priority" || tier == "fast"))
							if valid {
								if err != nil {
									t.Fatal(err)
								}
								if _, exists := body["service_tier"]; exists {
									t.Fatal("service tier reached upstream body")
								}
								if body["reasoning_effort"] != "high" || body["stream"] != stream || body["model"] != DefaultUpstreamModel {
									t.Fatal("unrelated request settings changed")
								}
							} else if api, ok := err.(*APIError); !ok || api.Status != 400 || api.Kind != "unsupported_service_tier" {
								t.Fatalf("expected unsupported_service_tier, got %v", err)
							}
							if !bytes.Equal(req.Payload, raw) || (original && !bytes.Equal(req.OriginalRequest, raw)) {
								t.Fatal("source request was mutated")
							}
						})
					}
				}
			}
		}
	}
}

func TestIgnoreFastTierConfiguration(t *testing.T) {
	s := NewService()
	for _, enabled := range []bool{true, false} {
		raw, _ := json.Marshal(map[string]any{"config_yaml": []byte(fmt.Sprintf("data_dir: \"\"\nignore_fast_tier: %t\n", enabled))})
		if err := s.configure(raw); err != nil {
			t.Fatal(err)
		}
		if s.config().IgnoreFastTier != enabled || s.status()["ignore_fast_tier"] != enabled {
			t.Fatal("tier setting was not applied")
		}
	}
}
