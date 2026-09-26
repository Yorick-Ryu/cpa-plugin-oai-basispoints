package basispoints

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The draft only streams message items. Tool/other items remain private until
// terminal validation enforces allowed tools, schemas, duplicates and parallel
// call policy. Original output indexes are retained even when events interleave.
// No upstream retry occurs once this path starts; visible text cannot be undone.
type incrementalMessage struct {
	id    string
	added map[string]any
	done  map[string]any
	parts map[int]*incrementalPart
}
type incrementalPart struct {
	kind     string
	text     strings.Builder
	textDone bool
	done     bool
}
type incrementalState struct {
	source       map[string]any
	send         func(map[string]any) error
	responseID   string
	started      bool
	terminal     map[string]any
	messages     map[int]*incrementalMessage
	terminalSeen bool
}

func streamText(value any) string { text, _ := value.(string); return text }

func streamProtocolError(reason string) error {
	return fail(502, "invalid_upstream_stream", "Basis Points stream contract violation: "+reason)
}
func streamIndex(value any) (int, error) {
	n, err := strconv.Atoi(fmt.Sprint(value))
	if err != nil || n < 0 || n > 100000 {
		return 0, streamProtocolError("invalid index")
	}
	return n, nil
}
func (st *incrementalState) emit(event map[string]any) error { return st.send(event) }
func (st *incrementalState) begin(response map[string]any) error {
	id := stringValue(response["id"])
	if id == "" {
		return streamProtocolError("missing response id")
	}
	if st.started {
		if st.responseID != id {
			return streamProtocolError("response id changed")
		}
		return nil
	}
	st.responseID = id
	st.started = true
	created := cloneObject(response)
	created["status"] = "in_progress"
	created["output"] = []any{}
	delete(created, "usage")
	if err := st.emit(map[string]any{"type": "response.created", "response": created}); err != nil {
		return err
	}
	return st.emit(map[string]any{"type": "response.in_progress", "response": created})
}
func (st *incrementalState) event(eventName, data string) error {
	if strings.TrimSpace(data) == "[DONE]" {
		return nil
	}
	event, reason := parseRelayObject(data)
	if reason != "" {
		return streamProtocolError("invalid event JSON")
	}
	kind := stringValue(event["type"])
	if kind == "" {
		kind = eventName
		event["type"] = kind
	}
	if st.terminalSeen {
		return streamProtocolError("event after terminal response")
	}
	switch kind {
	case "error", "response.failed", "response.cancelled":
		return fail(502, "upstream_response_failed", "Basis Points stream reported a failure")
	case "response.created", "response.in_progress":
		return st.begin(objectValue(event["response"]))
	case "response.completed", "response.incomplete":
		response, err := terminalResponse(objectValue(event["response"]))
		if err != nil {
			return err
		}
		if stringValue(response["status"]) != strings.TrimPrefix(kind, "response.") {
			return streamProtocolError("terminal status mismatch")
		}
		if err = st.begin(response); err != nil {
			return err
		}
		st.terminalSeen = true
		st.terminal = response
		return nil
	}
	if !st.started {
		return streamProtocolError("item before response start")
	}
	if kind == "response.output_item.added" {
		item := objectValue(event["item"])
		if stringValue(item["type"]) != "message" {
			return nil
		}
		index, err := streamIndex(event["output_index"])
		if err != nil {
			return err
		}
		id := stringValue(item["id"])
		if id == "" {
			return streamProtocolError("missing message id")
		}
		if st.messages[index] != nil {
			return streamProtocolError("duplicate message index")
		}
		if content, ok := item["content"].([]any); ok && len(content) > 0 {
			return streamProtocolError("nonempty added message")
		}
		st.messages[index] = &incrementalMessage{id: id, added: cloneObject(item), parts: map[int]*incrementalPart{}}
		return st.emit(event)
	}
	// Tool argument deltas and all other item-specific events are intentionally
	// held until the whole tool set is validated. Nothing executable leaks early.
	if kind == "response.function_call_arguments.delta" || kind == "response.function_call_arguments.done" || kind == "response.custom_tool_call_input.delta" || kind == "response.custom_tool_call_input.done" {
		return nil
	}
	supported := kind == "response.output_item.done" || kind == "response.content_part.added" || kind == "response.content_part.done" || kind == "response.output_text.delta" || kind == "response.output_text.done" || kind == "response.refusal.delta" || kind == "response.refusal.done" || kind == "response.output_text.annotation.added"
	if !supported {
		return nil
	}
	index, err := streamIndex(event["output_index"])
	if err != nil {
		return err
	}
	msg := st.messages[index]
	if msg == nil {
		// Non-message items are emitted later from the validated terminal response.
		if kind == "response.output_item.done" && stringValue(objectValue(event["item"])["type"]) != "message" {
			return nil
		}
		return streamProtocolError("message event without added item")
	}
	if msg.done != nil {
		return streamProtocolError("event after message done")
	}
	if kind == "response.output_item.done" {
		item := objectValue(event["item"])
		if stringValue(item["id"]) != msg.id || stringValue(item["type"]) != "message" {
			return streamProtocolError("message identity mismatch")
		}
		if err := verifyIncrementalMessage(msg, item); err != nil {
			return err
		}
		msg.done = cloneObject(item)
		return st.emit(event)
	}
	if stringValue(event["item_id"]) != msg.id {
		return streamProtocolError("item id mismatch")
	}
	partIndex, err := streamIndex(event["content_index"])
	if err != nil {
		return err
	}
	part := msg.parts[partIndex]
	if kind == "response.content_part.added" {
		if part != nil {
			return streamProtocolError("duplicate content part")
		}
		p := objectValue(event["part"])
		typ := stringValue(p["type"])
		if typ != "output_text" && typ != "refusal" {
			return streamProtocolError("unsupported incremental message content")
		}
		field := "text"
		if typ == "refusal" {
			field = "refusal"
		}
		if streamText(p[field]) != "" {
			return streamProtocolError("nonempty added content")
		}
		msg.parts[partIndex] = &incrementalPart{kind: typ}
		return st.emit(event)
	}
	if part == nil || part.done {
		return streamProtocolError("event outside content lifecycle")
	}
	switch kind {
	case "response.output_text.delta", "response.refusal.delta":
		if part.textDone {
			return streamProtocolError("delta after text done")
		}
		if (kind == "response.output_text.delta") != (part.kind == "output_text") {
			return streamProtocolError("delta kind mismatch")
		}
		delta, ok := event["delta"].(string)
		if !ok {
			return streamProtocolError("invalid text delta")
		}
		part.text.WriteString(delta)
	case "response.output_text.done", "response.refusal.done":
		if part.textDone {
			return streamProtocolError("duplicate text done")
		}
		field := "text"
		if part.kind == "refusal" {
			field = "refusal"
		}
		if (kind == "response.output_text.done") != (part.kind == "output_text") || streamText(event[field]) != part.text.String() {
			return streamProtocolError("text done mismatch")
		}
		part.textDone = true
	case "response.content_part.done":
		p := objectValue(event["part"])
		field := "text"
		if part.kind == "refusal" {
			field = "refusal"
		}
		if !part.textDone || stringValue(p["type"]) != part.kind || streamText(p[field]) != part.text.String() {
			return streamProtocolError("content done mismatch")
		}
		part.done = true
	}
	return st.emit(event)
}
func verifyIncrementalMessage(msg *incrementalMessage, item map[string]any) error {
	content, ok := item["content"].([]any)
	if !ok || len(content) != len(msg.parts) {
		return streamProtocolError("message content count mismatch")
	}
	for i, value := range content {
		part := msg.parts[i]
		p := objectValue(value)
		if part == nil || !part.done || !part.textDone {
			return streamProtocolError("incomplete message content")
		}
		field := "text"
		if part.kind == "refusal" {
			field = "refusal"
		}
		if stringValue(p["type"]) != part.kind || streamText(p[field]) != part.text.String() {
			return streamProtocolError("final text differs from streamed text")
		}
	}
	return nil
}
func (st *incrementalState) finish() error {
	if !st.terminalSeen {
		return streamProtocolError("stream ended without terminal response")
	}
	stripCacheCreationUsage(st.terminal)
	// Validate all tool calls atomically before any tool can reach the client.
	_, response, _, err := transformResponseBody(jsonBytes(st.terminal), st.source)
	if err != nil {
		return err
	}
	output, _ := response["output"].([]any)
	for index, msg := range st.messages {
		if index >= len(output) || msg.done == nil {
			return streamProtocolError("missing completed message")
		}
		if !bytes.Equal(jsonBytes(msg.done), jsonBytes(output[index])) {
			return streamProtocolError("terminal message changed")
		}
	}
	for index, item := range output {
		if st.messages[index] != nil {
			continue
		}
		// Reuse the tested native tool lifecycle, rewriting its local index only.
		synthetic := syntheticStream(map[string]any{"id": st.responseID, "status": "completed", "output": []any{item}})
		decoder := newSSEDecoder()
		err := decoder.feed(synthetic, func(name, data string) error {
			if data == "[DONE]" {
				return nil
			}
			event, reason := parseRelayObject(data)
			if reason != "" {
				return streamProtocolError("invalid synthetic event")
			}
			kind := stringValue(event["type"])
			if kind == "response.created" || kind == "response.in_progress" || kind == "response.completed" {
				return nil
			}
			event["output_index"] = index
			return st.emit(event)
		})
		if err != nil {
			return err
		}
	}
	return st.emit(map[string]any{"type": "response." + stringValue(response["status"]), "response": response})
}

func (s *Service) executeIncrementalStream(request ExecutorRequest, body map[string]any, c credential, source map[string]any) (any, error) {
	if request.StreamID == "" {
		return nil, fail(500, "stream_id_missing", "executor.execute_stream requires stream_id")
	}
	upstream, err := s.upstreamStream(request, body, c)
	if err != nil {
		return nil, err
	}
	cfg := s.config()
	go func() {
		defer func() { _ = s.call("host.http.stream_close", map[string]any{"stream_id": upstream.StreamID}, nil) }()
		sequence := 0
		send := func(event map[string]any) error {
			// Bind the validated session before exposing its terminal event, so a
			// following Alpha Search stays on the same account as inference.
			kind := stringValue(event["type"])
			if kind == "response.completed" || kind == "response.incomplete" {
				s.rememberSearchAccount(request, c)
			}
			event = cloneObject(event)
			event["sequence_number"] = sequence
			sequence++
			var payload []byte
			if request.Format == "codex" {
				payload = append([]byte("data: "), jsonBytes(event)...)
			} else {
				var b strings.Builder
				writeSSE(&b, stringValue(event["type"]), event)
				payload = []byte(b.String())
			}
			return s.call("host.stream.emit", map[string]any{"stream_id": request.StreamID, "payload": payload}, nil)
		}
		state := &incrementalState{source: source, send: send, messages: map[int]*incrementalMessage{}}
		decoder := newSSEDecoder()
		deadline := time.Now().Add(time.Duration(cfg.TimeoutSeconds) * time.Second)
		total := 0
		var prefix bytes.Buffer
		mode := ""
		read := func() error {
			for {
				if time.Now().After(deadline) {
					return timeoutError(cfg)
				}
				var chunk streamChunk
				if err := s.call("host.http.stream_read", map[string]any{"stream_id": upstream.StreamID}, &chunk); err != nil {
					return fail(502, "upstream_transport", "Basis Points stream read failed")
				}
				if chunk.Error != "" {
					return fail(502, "upstream_transport", "Basis Points stream interrupted")
				}
				total += len(chunk.Payload)
				if total > cfg.MaxResponseBytes {
					return fail(502, "upstream_response_too_large", "Basis Points response exceeds configured limit")
				}
				data := chunk.Payload
				if mode == "" {
					prefix.Write(data)
					trimmed := bytes.TrimSpace(prefix.Bytes())
					if len(trimmed) > 0 {
						if trimmed[0] == '{' {
							mode = "json"
						} else {
							mode = "sse"
							data = append([]byte(nil), prefix.Bytes()...)
							prefix.Reset()
						}
					} else {
						data = nil
					}
				} else if mode == "json" {
					prefix.Write(data)
				}
				if mode == "sse" {
					if err := decoder.feed(data, state.event); err != nil {
						return err
					}
				}
				if chunk.Done {
					if mode == "json" {
						response, err := parseResponse(prefix.Bytes(), http.Header{"Content-Type": {"application/json"}})
						if err != nil {
							return err
						}
						if err = state.event("", string(jsonBytes(map[string]any{"type": "response." + stringValue(response["status"]), "response": response}))); err != nil {
							return err
						}
					} else if err := decoder.feed([]byte("\n\n"), state.event); err != nil {
						return err
					}
					return state.finish()
				}
			}
		}
		err := read()
		closePayload := map[string]any{"stream_id": request.StreamID}
		if err != nil {
			kind, status := "upstream_stream_error", http.StatusBadGateway
			if api, ok := err.(*APIError); ok {
				kind, status = api.Code(), api.StatusCode()
			}
			// The host close ABI accepts only a string, so status would otherwise
			// disappear before credential-health classification. Preserve the
			// existing request-scoped 422 semantics for invalid tool relays only.
			// Transport, authentication and rate-limit failures keep their class.
			event := incrementalErrorEvent(kind, status, safeError(err))
			_ = send(event)
			closePayload["error"] = string(jsonBytes(event))
		} else if request.Format != "codex" {
			if err = s.call("host.stream.emit", map[string]any{"stream_id": request.StreamID, "payload": []byte("data: [DONE]\n\n")}, nil); err != nil {
				closePayload["error"] = "client disconnected"
			}
		}
		_ = s.call("host.stream.close", closePayload, nil)
	}()
	return map[string]any{"Headers": map[string][]string{"Content-Type": {"text/event-stream"}, "Cache-Control": {"no-cache"}}}, nil
}

func incrementalErrorEvent(kind string, status int, message string) map[string]any {
	errorType := "server_error"
	if kind == "invalid_tool_call" && status == http.StatusUnprocessableEntity {
		errorType = "invalid_request_error"
	}
	return map[string]any{"type": "error", "status": status, "error": map[string]any{
		"type": errorType, "code": kind, "message": message,
	}}
}
