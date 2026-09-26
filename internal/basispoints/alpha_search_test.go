package basispoints

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func searchTestService(t *testing.T) *Service {
	t.Helper()
	s := NewService()
	s.cfg.AlphaSearchSameAccount = true
	s.cfg.DataDir = t.TempDir()
	return s
}

func searchTestRoute(t *testing.T, s *Service, session string) map[string]any {
	t.Helper()
	result, err := s.Handle("model.route", jsonBytes(map[string]any{"SourceFormat": "codex-alpha-search", "RequestedModel": DefaultModelID, "Body": jsonBytes(map[string]any{"id": session, "model": DefaultModelID})}))
	if err != nil {
		t.Fatal(err)
	}
	return result.(map[string]any)
}

func TestAlphaSearchPreservesOriginalCredentialAndScopesPrefix(t *testing.T) {
	s := searchTestService(t)
	s.cfg.AllowedEmails = []string{"owner@example.test"}
	for _, email := range []string{"owner@example.test", "unrelated@example.test"} {
		raw := jsonBytes(map[string]any{"type": "codex", "access_token": "fixture-token", "account_id": "account-a", "email": email})
		result, err := s.Handle("auth.parse", jsonBytes(authParseRequest{Provider: "codex", FileName: "fixture.json", RawJSON: raw}))
		if err != nil {
			t.Fatal(err)
		}
		response := result.(map[string]any)
		if email != "owner@example.test" {
			if response["Auth"].(map[string]any)["Prefix"] != nil {
				t.Fatal("unrelated account changed")
			}
			continue
		}
		auths := response["Auths"].([]any)
		if len(auths) != 2 {
			t.Fatal("duplicate accounts introduced")
		}
		native := auths[0].(map[string]any)
		if native["Prefix"] != searchAccountPrefix("account-a") {
			t.Fatal("missing same-account prefix")
		}
		if string(native["StorageJSON"].([]byte)) != string(raw) {
			t.Fatal("credential changed")
		}
		if auths[1].(map[string]any)["Prefix"] != nil {
			t.Fatal("Basis Points execution routing changed")
		}
	}
}

func TestAlphaSearchSameAccountSurvivesReloadAndDoesNotFallback(t *testing.T) {
	s := searchTestService(t)
	for _, pair := range [][2]string{{"session-a", "account-a"}, {"session-b", "account-b"}} {
		s.rememberSearchAccount(ExecutorRequest{Model: DefaultModelID, OriginalRequest: jsonBytes(map[string]any{"prompt_cache_key": pair[0]})}, credential{AccountID: pair[1], AccessToken: "never-store-me"})
	}
	next := NewService()
	next.cfg = s.cfg
	for _, service := range []*Service{s, next} {
		for _, pair := range [][2]string{{"session-a", "account-a"}, {"session-b", "account-b"}} {
			route := searchTestRoute(t, service, pair[0])
			if route["Target"] != "codex" || route["TargetModel"] != searchAccountPrefix(pair[1])+"/"+DefaultUpstreamModel {
				t.Fatalf("wrong account route: %v", route)
			}
		}
		if route := searchTestRoute(t, service, "unknown"); route["TargetModel"] != searchPrefix+"unbound/"+DefaultUpstreamModel {
			t.Fatal("unbound session can fall back")
		}
	}
	path := filepath.Join(s.cfg.DataDir, "alpha-search-bindings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"never-store-me", "session-a", "account-a"} {
		if strings.Contains(string(data), sensitive) {
			t.Fatal("unhashed identity or token persisted")
		}
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal("binding permissions")
	}
}

func TestAlphaSearchLeavesOtherRequestsAndDisabledModeAlone(t *testing.T) {
	s := searchTestService(t)
	for _, source := range []string{"openai-response", "codex", "openai"} {
		r, err := s.routeAlphaSearch(jsonBytes(map[string]any{"SourceFormat": source, "RequestedModel": DefaultModelID}))
		if err != nil || r.(map[string]any)["Handled"] != false {
			t.Fatal("ordinary requests intercepted")
		}
	}
	r, _ := s.routeAlphaSearch(jsonBytes(map[string]any{"SourceFormat": "codex-alpha-search", "RequestedModel": "gpt-5.6-luna"}))
	if r.(map[string]any)["Handled"] != false {
		t.Fatal("native search changed")
	}
	s.cfg.AlphaSearchSameAccount = false
	if searchTestRoute(t, s, "anything")["Handled"] != false {
		t.Fatal("disabled search active")
	}
}

func TestAlphaSearchConcurrentSessionsAndExpiry(t *testing.T) {
	s := searchTestService(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i))
			s.rememberSearchAccount(ExecutorRequest{Model: DefaultModelID, Headers: http.Header{"Session_id": {id}}}, credential{AccountID: id})
			if searchTestRoute(t, s, id)["TargetModel"] != searchAccountPrefix(id)+"/"+DefaultUpstreamModel {
				t.Error("cross-session credential selection")
			}
		}(i)
	}
	wg.Wait()
	s.searchAccounts.mu.Lock()
	s.searchAccounts.entries[searchBindingKey("a", DefaultModelID)] = searchAccountBinding{Prefix: searchAccountPrefix("a"), Seen: time.Now().Add(-2 * searchBindingTTL)}
	s.searchAccounts.mu.Unlock()
	if searchTestRoute(t, s, "a")["TargetModel"] != searchPrefix+"unbound/"+DefaultUpstreamModel {
		t.Fatal("expired account selected")
	}
}

func TestAlphaSearchRejectsForcePrefixAndCorruptBinding(t *testing.T) {
	s := searchTestService(t)
	raw := jsonBytes(map[string]any{"Provider": "codex", "FileName": "fixture.json", "RawJSON": jsonBytes(map[string]any{"access_token": "fixture", "account_id": "acct"}), "Host": map[string]any{"ForceModelPrefix": true}})
	if _, err := s.parseSearchAccounts(raw); err == nil {
		t.Fatal("would remove normal unprefixed models")
	}
	_ = os.WriteFile(filepath.Join(s.cfg.DataDir, "alpha-search-bindings.json"), jsonBytes(map[string]any{searchBindingKey("x", DefaultModelID): map[string]any{"prefix": "free-account", "seen": time.Now()}}), 0600)
	if searchTestRoute(t, s, "x")["TargetModel"] != searchPrefix+"unbound/"+DefaultUpstreamModel {
		t.Fatal("invalid account prefix accepted")
	}
	var r map[string]any
	if json.Unmarshal(jsonBytes(registration(s.cfg)), &r) != nil || r["capabilities"].(map[string]any)["model_router"] != true {
		t.Fatal("model router not registered")
	}
}

func TestAlphaSearchPrefixesStayOutOfPublicModelCatalog(t *testing.T) {
	s := searchTestService(t)
	for _, field := range []string{"data", "models"} {
		id := "id"
		if field == "models" {
			id = "slug"
		}
		body := jsonBytes(map[string]any{field: []any{map[string]any{id: searchAccountPrefix("account") + "/gpt-6-astra"}, map[string]any{id: "gpt-6-astra"}}})
		r, err := s.interceptModelCatalog(jsonBytes(catalogInterceptRequest{SourceFormat: "openai", StatusCode: 200, Body: body}))
		if err != nil {
			t.Fatal(err)
		}
		clean := r.(map[string]any)["Body"].([]byte)
		if strings.Contains(string(clean), searchPrefix) || !strings.Contains(string(clean), "gpt-6-astra") {
			t.Fatal("public catalog leaked routing prefix")
		}
	}
}
