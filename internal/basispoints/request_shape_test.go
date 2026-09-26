package basispoints

import (
	"strings"
	"testing"
)

func TestRequestShapeNeverIncludesClientValues(t *testing.T) {
	secret := "secret-client-value"
	body := map[string]any{"input": []any{map[string]any{"type": secret, "encrypted_content": secret, "content": []any{map[string]any{"type": secret, "text": secret}}}}, "context_management": []any{map[string]any{"type": "compaction", "compact_threshold": secret}}}
	result := requestShape(body)
	if strings.Contains(result, secret) || !strings.Contains(result, "encrypted_items=1") || !strings.Contains(result, "threshold=-1") {
		t.Fatal("unsafe or missing structural diagnostic")
	}
}
