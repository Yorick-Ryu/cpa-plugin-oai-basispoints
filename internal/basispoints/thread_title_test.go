package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

func threadTitleSource() map[string]any {
	return map[string]any{"text": map[string]any{"format": map[string]any{
		"type": "json_schema", "name": "thread_title",
		"schema": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}, "required": []any{"title"}, "additionalProperties": false},
	}}}
}

func threadTitleResponse(text string) []byte {
	return jsonBytes(map[string]any{"id": "resp_title", "status": "completed", "output": []any{
		map[string]any{"id": "msg_title", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text}}},
	}})
}

func TestThreadTitleResponseFormatting(t *testing.T) {
	for _, input := range []string{"  修复 Codex 会话标题  ", `{"title":"修复 Codex 会话标题"}`} {
		body, response, replaced, err := transformResponseBody(threadTitleResponse(input), threadTitleSource())
		if err != nil || replaced {
			t.Fatalf("transform failed: replaced=%v err=%v", replaced, err)
		}
		output := response["output"].([]any)[0].(map[string]any)
		content := output["content"].([]any)[0].(map[string]any)
		var title map[string]string
		if err := json.Unmarshal([]byte(content["text"].(string)), &title); err != nil || title["title"] != "修复 Codex 会话标题" {
			t.Fatalf("invalid title JSON: %v %v", title, err)
		}
		if !strings.Contains(string(body), `\"title\"`) || !strings.Contains(string(syntheticStream(response)), "response.completed") {
			t.Fatal("title missing from response or stream")
		}
	}
}

func TestThreadTitleResponseRequiresText(t *testing.T) {
	for _, input := range []string{"", "  "} {
		if _, _, _, err := transformResponseBody(threadTitleResponse(input), threadTitleSource()); err == nil {
			t.Fatal("empty title accepted")
		}
	}
}

func TestThreadTitleFormattingIsScoped(t *testing.T) {
	plain := threadTitleResponse("普通答复")
	body, _, _, err := transformResponseBody(plain, map[string]any{})
	if err != nil || string(body) != string(plain) {
		t.Fatalf("ordinary response changed: %v", err)
	}
	if !isThreadTitleRequest(threadTitleSource()) {
		t.Fatal("Codex title schema not recognized")
	}
	source := threadTitleSource()
	source["text"].(map[string]any)["format"].(map[string]any)["name"] = "other"
	if isThreadTitleRequest(source) {
		t.Fatal("unrelated schema recognized as title")
	}
}
