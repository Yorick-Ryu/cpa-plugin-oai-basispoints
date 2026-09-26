package basispoints

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func draftMessage(id, text string) map[string]any {
	return map[string]any{"id": id, "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}
func draftResponse(output ...any) map[string]any {
	return map[string]any{"id": "resp_draft", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "input_tokens_details": map[string]any{"cached_tokens": 40, "cache_write_tokens": 30}}}
}
func draftEvents(t *testing.T, response map[string]any) []map[string]any {
	t.Helper()
	var events []map[string]any
	err := newSSEDecoder().feed(syntheticStream(response), func(_, data string) error {
		if data == "[DONE]" {
			return nil
		}
		e, why := parseRelayObject(data)
		if why != "" {
			return errors.New(why)
		}
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return events
}
func draftFrames(events []map[string]any) []byte {
	var b strings.Builder
	for _, e := range events {
		writeSSE(&b, stringValue(e["type"]), e)
	}
	return []byte(b.String())
}
func draftToolEvent(e map[string]any) bool {
	k := stringValue(e["type"])
	return strings.HasPrefix(k, "response.function_call_arguments") || strings.HasPrefix(k, "response.custom_tool_call_input") || strings.Contains(stringValue(objectValue(e["item"])["type"]), "call")
}
func TestDraftStateTextImmediateToolsAtomic(t *testing.T) {
	for _, typ := range []string{"function", "custom"} {
		for _, status := range []string{"completed", "incomplete"} {
			t.Run(typ+"/"+status, func(t *testing.T) {
				source := namespaceTestSource(typ, "execute", "demo")
				var args any = map[string]any{"code": "  你好\n"}
				if typ == "custom" {
					args = "  你好\r\n\t"
				}
				native := namespaceTestNative(t.Name(), "demo.execute", args)
				response := draftResponse(draftMessage("msg_one", "first 文本"), native, draftMessage("msg_two", "second"))
				response["status"] = status
				var sent []map[string]any
				st := &incrementalState{source: source, messages: map[int]*incrementalMessage{}, send: func(e map[string]any) error { sent = append(sent, cloneObject(e)); return nil }}
				for _, e := range draftEvents(t, response) {
					if err := st.event("", string(jsonBytes(e))); err != nil {
						t.Fatal(err)
					}
					for _, out := range sent {
						if draftToolEvent(out) {
							t.Fatal("tool escaped before terminal validation")
						}
					}
					if e["type"] == "response.output_text.delta" {
						if sent[len(sent)-1]["type"] != "response.output_text.delta" {
							t.Fatal("text was buffered")
						}
					}
				}
				if err := st.finish(); err != nil {
					t.Fatal(err)
				}
				var text string
				toolDone, terminal := 0, 0
				for _, e := range sent {
					if e["type"] == "response.output_text.delta" {
						text += streamText(e["delta"])
					}
					if e["type"] == "response.output_item.done" && draftToolEvent(e) {
						toolDone++
						if fmt.Sprint(e["output_index"]) != "1" {
							t.Fatal("tool index changed")
						}
					}
					if e["type"] == "response."+status {
						terminal++
						r := objectValue(e["response"])
						usage := objectValue(r["usage"])
						details := objectValue(usage["input_tokens_details"])
						if _, ok := details["cache_write_tokens"]; ok {
							t.Fatal("cache write leaked")
						}
						if fmt.Sprint(details["cached_tokens"]) != "40" || fmt.Sprint(usage["input_tokens"]) != "100" {
							t.Fatal("normal usage changed")
						}
						out := r["output"].([]any)
						if len(out) != 3 || objectValue(out[0])["id"] != "msg_one" || objectValue(out[2])["id"] != "msg_two" {
							t.Fatal("final output order changed")
						}
						field := "arguments"
						if typ == "custom" {
							field = "input"
						}
						if streamText(objectValue(out[1])[field]) != relayTestPayload(args) {
							t.Fatal("tool payload changed")
						}
					}
				}
				if text != "first 文本second" || toolDone != 1 || terminal != 1 {
					t.Fatalf("duplicate or missing events: %q %d %d", text, toolDone, terminal)
				}
			})
		}
	}
}
func TestDraftInvalidToolsNeverReachClient(t *testing.T) {
	for _, mode := range []string{"broken", "duplicate", "parallel", "required", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			source := namespaceTestSource("function", "execute", "")
			native := namespaceTestNative(t.Name(), "execute", map[string]any{"code": "ok"})
			output := []any{draftMessage("msg_text", "already visible"), native}
			switch mode {
			case "broken":
				native["arguments"] = "{"
			case "duplicate":
				output = append(output, cloneObject(native))
			case "parallel":
				source["parallel_tool_calls"] = false
				output = append(output, namespaceTestNative(t.Name()+"2", "execute", map[string]any{"code": "ok"}))
			case "required":
				source["tool_choice"] = "required"
				output = output[:1]
			case "unknown":
				native["arguments"] = string(jsonBytes(map[string]any{"code": "{}", "references": []any{"unavailable"}}))
			}
			var sent []map[string]any
			st := &incrementalState{source: source, messages: map[int]*incrementalMessage{}, send: func(e map[string]any) error { sent = append(sent, e); return nil }}
			for _, e := range draftEvents(t, draftResponse(output...)) {
				if err := st.event("", string(jsonBytes(e))); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.finish(); err == nil {
				t.Fatal("invalid tool set accepted")
			}
			for _, e := range sent {
				if draftToolEvent(e) || e["type"] == "response.completed" {
					t.Fatal("invalid tool or successful terminal leaked")
				}
			}
		})
	}
}
func TestDraftRejectsTruncationAndChangedFinalText(t *testing.T) {
	for _, mode := range []string{"truncated", "changed", "double_terminal", "bad_json", "wrong_id"} {
		t.Run(mode, func(t *testing.T) {
			response := draftResponse(draftMessage("msg_one", "safe"))
			events := draftEvents(t, response)
			if mode == "truncated" {
				events = events[:len(events)-1]
			}
			if mode == "changed" {
				r := objectValue(events[len(events)-1]["response"])
				r["output"] = []any{draftMessage("msg_one", "different")}
			}
			if mode == "double_terminal" {
				events = append(events, events[len(events)-1])
			}
			if mode == "wrong_id" {
				objectValue(events[len(events)-1]["response"])["id"] = "other"
			}
			st := &incrementalState{source: map[string]any{}, messages: map[int]*incrementalMessage{}, send: func(e map[string]any) error { return nil }}
			var err error
			for _, e := range events {
				err = st.event("", string(jsonBytes(e)))
				if err != nil {
					break
				}
			}
			if mode == "bad_json" {
				err = st.event("", "{")
			}
			if err == nil {
				err = st.finish()
			}
			if err == nil {
				t.Fatal("bad stream accepted")
			}
		})
	}
}

func TestDraftExecutorDeliversTextBeforeUpstreamCompletion(t *testing.T) {
	for _, format := range []string{"openai-response", "codex"} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", format, failure), func(t *testing.T) {
				source := namespaceTestSource("function", "execute", "")
				source["model"] = DefaultModelID
				source["input"] = "test"
				source["stream"] = true
				response := draftResponse(draftMessage("msg_text", "first delta"), namespaceTestNative(t.Name(), "execute", map[string]any{"code": "ok"}))
				events := draftEvents(t, response)
				split := 0
				for i, e := range events {
					if e["type"] == "response.output_text.delta" {
						split = i + 1
						break
					}
				}
				first, rest := draftFrames(events[:split]), draftFrames(events[split:])
				gate := make(chan struct{})
				defer func() {
					select {
					case <-gate:
					default:
						close(gate)
					}
				}()
				visible := make(chan struct{}, 1)
				closed := make(chan map[string]any, 1)
				upstreamClosed := make(chan struct{}, 1)
				var mu sync.Mutex
				var emitted []map[string]any
				requests, reads := 0, 0
				service := NewService()
				service.cfg.IncrementalTextStream = true
				service.cfg.AlphaSearchSameAccount = true
				service.cfg.DataDir = t.TempDir()
				service.SetHost(func(method string, payload any, out any) error {
					p := payload.(map[string]any)
					switch method {
					case "host.http.do_stream":
						requests++
						*out.(*upstreamStream) = upstreamStream{StatusCode: 200, StreamID: "upstream"}
					case "host.http.stream_read":
						reads++
						if reads == 1 {
							*out.(*streamChunk) = streamChunk{Payload: first}
						} else {
							<-gate
							if failure {
								*out.(*streamChunk) = streamChunk{Error: "fixture interruption"}
							} else {
								*out.(*streamChunk) = streamChunk{Payload: rest, Done: true}
							}
						}
					case "host.http.stream_close":
						upstreamClosed <- struct{}{}
					case "host.stream.emit":
						raw := p["payload"].([]byte)
						if format == "codex" {
							raw = append(bytes.Clone(raw), '\n', '\n')
						}
						return newSSEDecoder().feed(raw, func(_, data string) error {
							if data == "[DONE]" {
								return nil
							}
							e, why := parseRelayObject(data)
							if why != "" {
								return errors.New(why)
							}
							if e["type"] == "response.completed" {
								route := searchTestRoute(t, service, "draft-session")
								if route["TargetModel"] != searchAccountPrefix("fixture")+"/"+DefaultUpstreamModel {
									t.Error("terminal exposed before same-account search binding")
								}
							}
							mu.Lock()
							emitted = append(emitted, e)
							mu.Unlock()
							if e["type"] == "response.output_text.delta" {
								select {
								case visible <- struct{}{}:
								default:
								}
							}
							return nil
						})
					case "host.stream.close":
						closed <- p
					default:
						return fmt.Errorf("unexpected callback %s", method)
					}
					return nil
				})
				req := ExecutorRequest{Metadata: map[string]any{"canonical_session_id": "draft-session"}, Model: DefaultModelID, Payload: jsonBytes(source), Format: format, SourceFormat: "codex", Stream: true, StreamID: "client", StorageJSON: jsonBytes(map[string]any{"access_token": "fixture", "account_id": "fixture"})}
				returned := make(chan error, 1)
				go func() { _, err := service.Handle("executor.execute_stream", jsonBytes(req)); returned <- err }()
				select {
				case err := <-returned:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("executor blocked on full response")
				}
				select {
				case <-visible:
				case <-time.After(2 * time.Second):
					t.Fatal("text not delivered before completion gate")
				}
				mu.Lock()
				for _, e := range emitted {
					if draftToolEvent(e) {
						t.Error("unvalidated tool emitted")
					}
				}
				mu.Unlock()
				close(gate)
				var end map[string]any
				select {
				case end = <-closed:
				case <-time.After(2 * time.Second):
					t.Fatal("stream did not close")
				}
				select {
				case <-upstreamClosed:
				case <-time.After(2 * time.Second):
					t.Fatal("upstream stream leaked")
				}
				if failure != (end["error"] != nil) {
					t.Fatal("wrong close status")
				}
				if failure {
					route := searchTestRoute(t, service, "draft-session")
					if route["TargetModel"] != searchPrefix+"unbound/"+DefaultUpstreamModel {
						t.Fatal("failed incremental response bound search account")
					}
				}
				if requests != 1 {
					t.Fatal("stream was replayed")
				}
				mu.Lock()
				defer mu.Unlock()
				terminals, texts := 0, 0
				for i, e := range emitted {
					if e["type"] == "error" && fmt.Sprint(e["status"]) != "502" {
						t.Fatal("stream error lost HTTP status")
					}
					if fmt.Sprint(e["sequence_number"]) != fmt.Sprint(i) {
						t.Fatal("nonmonotonic sequence")
					}
					if e["type"] == "response.completed" {
						terminals++
					}
					if e["type"] == "response.output_text.delta" {
						texts++
					}
					if failure && draftToolEvent(e) {
						t.Fatal("tool leaked on failure")
					}
				}
				if texts != 1 || (!failure && terminals != 1) || (failure && terminals != 0) {
					t.Fatal("duplicated text or false terminal")
				}
			})
		}
	}
}
func TestDraftByteFragmentationAndTerminalOnly(t *testing.T) {
	for _, terminalOnly := range []bool{false, true} {
		response := draftResponse(draftMessage("msg_utf8", "汉字🙂\r\n"))
		events := draftEvents(t, response)
		if terminalOnly {
			events = events[len(events)-1:]
		}
		var got []map[string]any
		st := &incrementalState{source: map[string]any{}, messages: map[int]*incrementalMessage{}, send: func(e map[string]any) error { got = append(got, e); return nil }}
		decoder := newSSEDecoder()
		for _, b := range draftFrames(events) {
			if err := decoder.feed([]byte{b}, st.event); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.finish(); err != nil {
			t.Fatal(err)
		}
		var text string
		for _, e := range got {
			if e["type"] == "response.output_text.delta" {
				text += streamText(e["delta"])
			}
		}
		if text != "汉字🙂\r\n" {
			t.Fatal("fragmented Unicode changed")
		}
	}
}
func TestDraftOptionOffByDefault(t *testing.T) {
	s := NewService()
	if s.config().IncrementalTextStream {
		t.Fatal("experiment enabled by default")
	}
	raw, _ := json.Marshal(map[string]any{"config_yaml": []byte("data_dir: \"\"\nincremental_text_stream: true\n")})
	if err := s.configure(raw); err != nil {
		t.Fatal(err)
	}
	if !s.config().IncrementalTextStream {
		t.Fatal("option not parsed")
	}
}

func TestDraftErrorClassificationAcrossHostBridge(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		status int
		want   string
	}{
		{"invalid_tool_call", 422, "invalid_request_error"},
		{"invalid_tool_call", 500, "server_error"},
		{"upstream_stream_error", 502, "server_error"},
		{"authentication_error", 401, "server_error"},
		{"rate_limit_exceeded", 429, "server_error"},
	} {
		t.Run(fmt.Sprintf("%s_%d", tc.kind, tc.status), func(t *testing.T) {
			event := incrementalErrorEvent(tc.kind, tc.status, "fixture failure")
			var decoded map[string]any
			if err := json.Unmarshal(jsonBytes(event), &decoded); err != nil {
				t.Fatal(err)
			}
			body := decoded["error"].(map[string]any)
			if body["type"] != tc.want || body["code"] != tc.kind || decoded["status"] != float64(tc.status) {
				t.Fatalf("lost error classification: %#v", decoded)
			}
		})
	}
}
