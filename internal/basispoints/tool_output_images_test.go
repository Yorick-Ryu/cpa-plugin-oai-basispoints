package basispoints

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestToolOutputImagesUploadBeforeInference(t *testing.T) {
	dataURL, imageBytes := testImageDataURL(t)
	for _, toolType := range []string{"function", "custom"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", toolType, stream), func(t *testing.T) {
				service := NewService()
				uploads, responses := 0, 0
				var received map[string]any
				service.SetHost(func(method string, payload any, out any) error {
					wire := payload.(map[string]any)
					if strings.HasSuffix(wire["url"].(string), "/attachments") {
						uploads++
						assertImageUpload(t, payload, imageBytes)
						*out.(*upstreamResponse) = upstreamResponse{StatusCode: 200, Body: jsonBytes(map[string]any{"openai_file_id": "file-tool-image"})}
						return nil
					}
					responses++
					if err := json.Unmarshal(wire["body"].([]byte), &received); err != nil {
						return err
					}
					if stream {
						*out.(*upstreamStream) = upstreamStream{StatusCode: 200, StreamID: "test-stream"}
					} else {
						*out.(*upstreamResponse) = upstreamResponse{StatusCode: 200}
					}
					return nil
				})
				call := map[string]any{"type": toolType + "_tool_call", "name": "exec", "call_id": "call_tool_image", "input": "read image"}
				outputType := "custom_tool_call_output"
				if toolType == "function" {
					call = map[string]any{"type": "function_call", "name": "exec", "call_id": "call_tool_image", "arguments": "{}"}
					outputType = "function_call_output"
				}
				parts := []any{
					map[string]any{"type": "input_text", "text": "before image"},
					map[string]any{"type": "input_image", "image_url": dataURL, "detail": "high"},
					map[string]any{"type": "input_image", "image_url": dataURL},
					map[string]any{"type": "input_text", "text": "after image"},
				}
				request := imageRequest()
				request.Stream = stream
				// A legacy bare tool name deliberately has no matching namespace.
				request.Payload = jsonBytes(map[string]any{"input": []any{messageItem("user", "Inspect the result."), call, map[string]any{"type": outputType, "call_id": "call_tool_image", "output": parts}}})
				original := string(request.Payload)
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
				if uploads != 1 || responses != 1 {
					t.Fatalf("uploads=%d responses=%d; tool images must be uploaded and deduplicated", uploads, responses)
				}
				var result map[string]any
				resultIndex := -1
				history := received["input"].([]any)
				for index, value := range history {
					item := objectValue(value)
					if item["type"] == outputType {
						result = item
						resultIndex = index
					}
				}
				got := result["output"].([]any)
				want := []any{parts[0], map[string]any{"type": "input_image", "file_id": "file-tool-image", "detail": "high"}, map[string]any{"type": "input_image", "file_id": "file-tool-image", "detail": "auto"}, parts[3]}
				if toolType == "function" {
					if len(got) != 4 || !reflect.DeepEqual(got[0], parts[0]) || !reflect.DeepEqual(got[3], parts[3]) || !strings.Contains(stringValue(objectValue(got[1])["text"]), "image 1") || !strings.Contains(stringValue(objectValue(got[2])["text"]), "image 2") {
						t.Fatal("function result lost text or image positions")
					}
					attached := objectValue(history[resultIndex+1])
					content := attached["content"].([]any)
					if attached["role"] != "user" || !strings.Contains(stringValue(objectValue(content[0])["text"]), "call_tool_image") || !reflect.DeepEqual(content[1:], want[1:3]) {
						t.Fatal("function images lost their call identity, order, detail, or uploaded references")
					}
				} else if !reflect.DeepEqual(got, want) {
					t.Fatal("custom result image references changed unexpectedly")
				}
				if result["call_id"] != "call_tool_image" || string(request.Payload) != original {
					t.Fatal("tool result order, identity, detail, or source bytes changed")
				}
			})
		}
	}
}

func TestFunctionImageAdaptationPreservesReferencesAndCompactionOrder(t *testing.T) {
	images := []any{
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64,fixture", "detail": "original"},
		map[string]any{"type": "input_image", "file_id": "file-existing", "detail": "high"},
		map[string]any{"type": "input_image", "image_url": "https://example.test/image.png"},
	}
	trigger := map[string]any{"type": "compaction_trigger"}
	input := []any{
		map[string]any{"type": "compaction", "encrypted_content": "opaque-existing-state"},
		map[string]any{"type": "function_call_output", "call_id": "call_original", "id": "result_original", "output": images},
		trigger,
	}
	before := string(jsonBytes(input))
	body := map[string]any{"input": input}
	normalizeFunctionOutputImages(body)
	result := body["input"].([]any)
	if len(result) != 4 || !reflect.DeepEqual(result[0], input[0]) || !reflect.DeepEqual(result[3], trigger) || string(jsonBytes(input)) != before {
		t.Fatal("image adaptation changed prior state, the source input, or final compaction control")
	}
	toolResult := objectValue(result[1])
	if toolResult["call_id"] != "call_original" || toolResult["id"] != "result_original" || len(toolResult["output"].([]any)) != len(images) {
		t.Fatal("tool result identity or image markers changed")
	}
	attached := objectValue(result[2])["content"].([]any)
	if !reflect.DeepEqual(attached[1:], images) {
		t.Fatal("image data, references, detail, or order changed")
	}
}

func TestToolImageUploadFailurePreventsInference(t *testing.T) {
	dataURL, _ := testImageDataURL(t)
	service := NewService()
	uploads := 0
	service.SetHost(func(method string, payload any, out any) error {
		if !strings.HasSuffix(payload.(map[string]any)["url"].(string), "/attachments") {
			t.Fatal("inference submitted after failed image upload")
		}
		uploads++
		return errors.New("synthetic upload failure")
	})
	request := imageRequest()
	request.Payload = jsonBytes(map[string]any{"input": []any{map[string]any{"type": "function_call_output", "call_id": "call_image_failure", "output": []any{map[string]any{"type": "input_image", "image_url": dataURL}}}}})
	if _, _, err := service.prepareRequest(request); err == nil || uploads != 1 {
		t.Fatal("tool image upload error was not propagated")
	}
}

func TestUpstreamErrorCountsToolOutputImages(t *testing.T) {
	body := map[string]any{"input": []any{map[string]any{"type": "custom_tool_call_output", "output": []any{map[string]any{"type": "input_image", "image_url": "private-image-data", "detail": "original"}}}}}
	err := upstreamRequestError(422, []byte(`{"detail":"Invalid request body."}`), body, credential{})
	if !strings.Contains(err.Error(), "input_images=1") || !strings.Contains(err.Error(), "tool_output_images=1") || !strings.Contains(err.Error(), "original_detail_images=1") || strings.Contains(err.Error(), "private-image-data") {
		t.Fatal("error diagnostics missed tool images or exposed image data")
	}
}
