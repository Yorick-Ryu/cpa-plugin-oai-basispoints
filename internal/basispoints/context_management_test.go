package basispoints

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestContextManagementWireEncoding(t *testing.T) {
	cases := []struct {
		name        string
		source      string
		wantPresent bool
	}{
		{"omitted", `{"input":"Reply OK"}`, false},
		{"null", `{"input":"Reply OK","context_management":null}`, false},
		{"empty", `{"input":"Reply OK","context_management":[]}`, false},
		{"explicit-threshold", `{"input":"Reply OK","context_management":[{"type":"compaction","compact_threshold":475000}]}`, true},
		{"server-threshold", `{"input":"Reply OK","context_management":[{"type":"compaction"}]}`, true},
		{"null-threshold", `{"input":"Reply OK","context_management":[{"type":"compaction","compact_threshold":null}]}`, true},
		{"invalid-type-not-hidden", `{"input":"Reply OK","context_management":"invalid-policy"}`, true},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			for _, original := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/original=%t", tc.name, stream, original), func(t *testing.T) {
					service := NewService()
					calls := 0
					service.SetHost(func(method string, payload any, out any) error {
						calls++
						expectedMethod := "host.http.do"
						if stream {
							expectedMethod = "host.http.do_stream"
						}
						if method != expectedMethod {
							t.Fatalf("method = %s", method)
						}
						encoded := payload.(map[string]any)["body"].([]byte)
						var wire, source map[string]any
						if err := json.Unmarshal(encoded, &wire); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal([]byte(tc.source), &source); err != nil {
							t.Fatal(err)
						}
						policy, present := wire["context_management"]
						if present != tc.wantPresent {
							t.Fatalf("wire policy presence = %t, want %t; policy=%v", present, tc.wantPresent, policy)
						}
						expected := source["context_management"]
						if tc.name == "server-threshold" || tc.name == "null-threshold" {
							expected = []any{map[string]any{"type": "compaction", "compact_threshold": float64(DefaultCompactThreshold)}}
						}
						if present && !reflect.DeepEqual(policy, expected) {
							t.Fatalf("policy was changed: %v", policy)
						}
						if entries, ok := policy.([]any); present && ok && len(entries) == 0 {
							t.Fatal("sent empty context_management array")
						}
						if stream {
							*out.(*upstreamStream) = upstreamStream{StatusCode: 200, StreamID: "test-stream"}
						} else {
							*out.(*upstreamResponse) = upstreamResponse{StatusCode: 200}
						}
						return nil
					})
					request := ExecutorRequest{Model: DefaultModelID, Stream: stream, Payload: []byte(tc.source), StorageJSON: jsonBytes(map[string]any{"access_token": "test-access", "account_id": "test-account"})}
					if original {
						request.OriginalRequest = []byte(tc.source)
					}
					body, credential, err := service.prepareRequest(request)
					if err != nil {
						t.Fatal(err)
					}
					if stream {
						_, err = service.upstreamStream(request, body, credential)
					} else {
						_, err = service.upstreamRequest(request, body, credential, false)
					}
					if err != nil {
						t.Fatal(err)
					}
					if calls != 1 {
						t.Fatalf("host calls = %d, want 1", calls)
					}
				})
			}
		}
	}
}

func TestCompactionThresholdCompatibilityPreservesExplicitPolicy(t *testing.T) {
	for _, threshold := range []any{json.Number("475000"), json.Number("0"), json.Number("-1"), json.Number("1.5"), "invalid", false} {
		policy := []any{map[string]any{"type": "compaction", "compact_threshold": threshold}}
		if actual := normalizeContextManagement(policy, 200000); !reflect.DeepEqual(actual, policy) {
			t.Fatalf("explicit value was replaced: %v", threshold)
		}
	}
	policy := []any{
		map[string]any{"type": "compaction", "future_field": json.Number("9007199254740993")},
		map[string]any{"type": "future_policy"},
		"invalid-entry",
	}
	before := string(jsonBytes(policy))
	result := normalizeContextManagement(policy, 123456).([]any)
	if result[0].(map[string]any)["compact_threshold"] != 123456 {
		t.Fatal("configured fallback was not applied")
	}
	if result[0].(map[string]any)["future_field"] != json.Number("9007199254740993") || !reflect.DeepEqual(result[1:], policy[1:]) {
		t.Fatal("unrelated policy fields changed")
	}
	if string(jsonBytes(policy)) != before {
		t.Fatal("source policy was mutated")
	}
}

func TestCompactionThresholdConfiguration(t *testing.T) {
	s := NewService()
	for _, value := range []int{123456, 0, -1} {
		raw := jsonBytes(map[string]any{"config_yaml": []byte(fmt.Sprintf("data_dir: \"\"\ndefault_compact_threshold: %d\n", value))})
		err := s.configure(raw)
		if value <= 0 {
			if api, ok := err.(*APIError); !ok || api.Kind != "invalid_config" {
				t.Fatalf("expected invalid_config for %d, got %v", value, err)
			}
			continue
		}
		if err != nil || s.status()["default_compact_threshold"] != value {
			t.Fatalf("configuration not applied: %v", err)
		}
		source, _ := rawObject([]byte(`{"model":"gpt-6-astra-basispoints","input":"OK","context_management":[{"type":"compaction"}]}`))
		before := string(jsonBytes(source))
		body, err := prepareResponsesBody(source, s.config())
		if err != nil || body["context_management"].([]any)[0].(map[string]any)["compact_threshold"] != value {
			t.Fatalf("configured threshold not used by request path: %v", err)
		}
		if string(jsonBytes(source)) != before {
			t.Fatal("source request mutated")
		}
	}
	if err := s.configure(jsonBytes(map[string]any{"config_yaml": []byte("data_dir: \"\"\n")})); err != nil || s.config().DefaultCompactThreshold != DefaultCompactThreshold {
		t.Fatalf("omitted setting did not restore default: %v", err)
	}
}
