package basispoints

import "testing"

func TestAuthParseAllowedEmailsKeepsNativeCodex(t *testing.T) {
	allowed := []string{"target@example.com"}
	for _, tc := range []struct {
		provider string
		email    string
		want     int
	}{
		{AuthProviderID, "TARGET@example.com", 2},
		{AuthProviderID, "other@example.com", 1},
		{Provider, "other@example.com", 0},
	} {
		raw := jsonBytes(map[string]any{
			"type": "codex", "access_token": "test-access", "account_id": "test-account", "email": tc.email,
		})
		result, err := authParseWithAllowedEmails(jsonBytes(authParseRequest{
			Provider: tc.provider, FileName: "account.json", RawJSON: raw,
		}), allowed)
		if err != nil {
			t.Fatal(err)
		}
		if result["Handled"] != true {
			t.Fatalf("%s: auth was not handled", tc.email)
		}
		count := 0
		if auths, ok := result["Auths"].([]any); ok {
			count = len(auths)
		}
		if result["Auth"] != nil {
			count = 1
		}
		if count != tc.want {
			t.Fatalf("%s: got %d auths, want %d", tc.email, count, tc.want)
		}
		if tc.provider == AuthProviderID && tc.want == 1 {
			if objectValue(result["Auth"])["Provider"] != AuthProviderID {
				t.Fatal("non-allowed account lost native Codex auth")
			}
		}
	}
}

func TestAllowedEmailsNormalize(t *testing.T) {
	cfg := defaultConfig()
	cfg.AllowedEmails = []string{" TARGET@example.com ", "target@example.com", ""}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedEmails) != 1 || cfg.AllowedEmails[0] != "target@example.com" {
		t.Fatalf("normalized emails = %#v", cfg.AllowedEmails)
	}
}
