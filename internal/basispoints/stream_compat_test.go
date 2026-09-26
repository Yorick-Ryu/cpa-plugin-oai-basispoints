package basispoints

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise our compatibility behavior through the upstream stream executor,
// including ordering at the host boundary, instead of its private state types.
func TestStreamCompatibilityAcrossUpstreamMerge(t *testing.T) {
	for _, format := range []string{"openai-response", "codex"} {
		for _, mode := range []string{"incremental", "buffered", "thread_title", "codex_output_schema", "buffered_retry", "invalid_tools", "transport_failure"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				svc := searchTestService(t)
				svc.cfg.IncrementalTextStream = mode != "buffered" && mode != "buffered_retry"
				source := map[string]any{"model": DefaultModelID, "input": "hello", "prompt_cache_key": "fixture-session", "tools": []any{map[string]any{"type": "function", "name": "exec_command", "parameters": map[string]any{"type": "object"}}}}
				title := mode == "thread_title" || mode == "codex_output_schema"
				if title {
					source["text"] = threadTitleSourceNamed(mode)["text"]
				}
				text := "合并后的标题"
				response := incrementalTerminal(text)
				usage := map[string]any{"input_tokens": 100, "output_tokens": 10, "total_tokens": 110, "cache_creation_input_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 40, "cache_write_tokens": 20}}
				response["usage"] = usage
				prefix := bytes.Replace(incrementalPrefix(text), []byte(`"output":[]`), append([]byte(`"usage":`), append(jsonBytes(usage), []byte(`,"output":[]`)...)...), 1)
				bad, _ := relayFixture(t.Name()+"-bad", true)
				var frames [][]byte
				var callbackErr error
				attempts, reads, earlyFrames := 0, 0, 0
				terminalRead := false
				closed := make(chan struct{}, 1)
				boundAtTerminal := false
				svc.SetHost(func(method string, payload any, out any) error {
					switch method {
					case "host.http.do_stream":
						attempts++
						reads, terminalRead = 0, false
						*out.(*upstreamStream) = upstreamStream{StatusCode: 200, StreamID: "fixture-upstream"}
					case "host.http.stream_read":
						reads++
						if reads == 1 {
							*out.(*streamChunk) = streamChunk{Payload: prefix}
							return nil
						}
						terminalRead = true
						final := cloneObject(response)
						if mode == "invalid_tools" || (mode == "buffered_retry" && attempts == 1) {
							final["output"] = append(final["output"].([]any), bad)
						}
						chunk := streamChunk{Payload: streamFixtureEvents(map[string]any{"type": "response.completed", "response": final}), Done: true}
						if mode == "transport_failure" {
							chunk.Payload, chunk.Error = nil, "fixture interrupted"
						}
						*out.(*streamChunk) = chunk
					case "host.http.stream_close":
					case "host.stream.emit":
						frame := bytes.Clone(payload.(map[string]any)["payload"].([]byte))
						frames = append(frames, frame)
						if !terminalRead {
							earlyFrames++
						}
						if bytes.Contains(frame, []byte(`"type":"response.completed"`)) {
							route, err := svc.routeAlphaSearch(jsonBytes(map[string]any{"SourceFormat": "codex-alpha-search", "RequestedModel": DefaultModelID, "Body": jsonBytes(map[string]any{"id": "fixture-session"})}))
							callbackErr = err
							if err == nil {
								boundAtTerminal = route.(map[string]any)["TargetModel"] == searchAccountPrefix("fixture-account")+"/"+DefaultUpstreamModel
							}
						}
					case "host.stream.close":
						closed <- struct{}{}
					default:
						return fmt.Errorf("unexpected host method %s", method)
					}
					return nil
				})
				_, err := svc.Handle("executor.execute_stream", jsonBytes(ExecutorRequest{Model: DefaultModelID, Format: format, SourceFormat: format, Stream: true, StreamID: "fixture-client", Payload: jsonBytes(source), AuthMetadata: map[string]any{"access_token": "fixture", "account_id": "fixture-account"}}))
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("stream did not close")
				}
				svc.streamWG.Wait()
				if callbackErr != nil {
					t.Fatal(callbackErr)
				}
				buffered := !svc.cfg.IncrementalTextStream || title
				if (earlyFrames == 0) != buffered {
					t.Fatalf("buffered=%v early frames=%d", buffered, earlyFrames)
				}
				wantAttempts := 1
				if mode == "buffered_retry" {
					wantAttempts = 2
				}
				if attempts != wantAttempts {
					t.Fatalf("attempts=%d want=%d", attempts, wantAttempts)
				}
				failed := mode == "invalid_tools" || mode == "transport_failure"
				if boundAtTerminal == failed {
					t.Fatalf("failure=%v bound before terminal=%v", failed, boundAtTerminal)
				}
				if failed && searchTestRoute(t, svc, "fixture-session")["TargetModel"] != searchPrefix+"unbound/"+DefaultUpstreamModel {
					t.Fatal("failed response established search binding")
				}
				var actual string
				terminals, failures := 0, 0
				for _, event := range decodeIncrementalFrames(t, format, frames) {
					if strings.Contains(string(jsonBytes(event)), "cache_write_tokens") || strings.Contains(string(jsonBytes(event)), "cache_creation_input_tokens") {
						t.Fatal("cache creation usage leaked")
					}
					if event["type"] == "response.output_text.delta" {
						actual += stringValue(event["delta"])
					}
					if event["type"] == "error" {
						failures++
					}
					if event["type"] == "response.completed" {
						terminals++
						u := objectValue(objectValue(event["response"])["usage"])
						if u["input_tokens"] != float64(100) || u["output_tokens"] != float64(10) || u["total_tokens"] != float64(110) || objectValue(u["input_tokens_details"])["cached_tokens"] != float64(40) {
							t.Fatal("normal usage changed", u)
						}
					}
				}
				if (failed && (terminals != 0 || failures != 1)) || (!failed && (terminals != 1 || failures != 0)) {
					t.Fatalf("terminals=%d failures=%d", terminals, failures)
				}
				if title {
					var value map[string]string
					if json.Unmarshal([]byte(actual), &value) != nil || value["title"] != text {
						t.Fatalf("title is not valid JSON: %q", actual)
					}
				} else if actual != text {
					t.Fatalf("text changed: %q", actual)
				}
			})
		}
	}
}

func TestIncrementalSettingDefaultAndPersistence(t *testing.T) {
	if !NewService().config().IncrementalTextStream {
		t.Fatal("new installs must use upstream incremental behavior")
	}
	dir := t.TempDir()
	raw := jsonBytes(map[string]any{"config_yaml": []byte(fmt.Sprintf("data_dir: %q\nincremental_text_stream: false\n", dir))})
	svc := NewService()
	if err := svc.configure(raw); err != nil {
		t.Fatal(err)
	}
	if svc.config().IncrementalTextStream {
		t.Fatal("explicit false ignored")
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
	next := NewService()
	if err := next.configure(jsonBytes(map[string]any{"config_yaml": []byte(fmt.Sprintf("data_dir: %q\n", dir))})); err != nil {
		t.Fatal(err)
	}
	if next.config().IncrementalTextStream {
		t.Fatal("persisted false was reset to the new default")
	}
}
