package basispoints

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const searchPrefix = "bps-search-"
const searchBindingTTL = 24 * time.Hour
const maxSearchBindings = 4096

// Account-specific search routes are transport details, not public models.
func hideSearchRoutes(body []byte, enabled bool) ([]byte, bool) {
	if !enabled {
		return body, false
	}
	var catalog map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil {
		return body, false
	}
	changed := false
	for _, field := range []string{"data", "models"} {
		var entries []json.RawMessage
		if json.Unmarshal(catalog[field], &entries) != nil {
			continue
		}
		out := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			var item struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
			}
			_ = json.Unmarshal(entry, &item)
			name := item.ID
			if item.Slug != "" {
				name = item.Slug
			}
			prefix, _, _ := strings.Cut(name, "/")
			if validSearchAccountPrefix(prefix) {
				changed = true
				continue
			}
			out = append(out, entry)
		}
		catalog[field] = jsonBytes(out)
	}
	if !changed {
		return body, false
	}
	return jsonBytes(catalog), true
}

type searchAccountBinding struct {
	Prefix string    `json:"prefix"`
	Seen   time.Time `json:"seen"`
}

// Only hashed session keys and opaque account prefixes are retained.
type searchAccountBindings struct {
	mu      sync.Mutex
	loaded  bool
	entries map[string]searchAccountBinding
}

func searchAccountPrefix(accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return searchPrefix + hex.EncodeToString(sum[:16])
}

// CPA retains unprefixed models when force-model-prefix is false. The additional
// prefix gives its existing selector an exact, same-account search target.
func (s *Service) parseSearchAccounts(raw json.RawMessage) (any, error) {
	cfg := s.config()
	result, err := authParseWithAllowedEmails(raw, cfg.AllowedEmails)
	if err != nil || !cfg.AlphaSearchSameAccount {
		return result, err
	}
	auths, ok := result["Auths"].([]any)
	if !ok {
		return result, nil
	}
	var native map[string]any
	hasBasisPoints := false
	for _, v := range auths {
		a, _ := v.(map[string]any)
		if a["Provider"] == Provider {
			hasBasisPoints = true
		}
		if a["Provider"] == AuthProviderID {
			native = a
		}
	}
	if !hasBasisPoints || native == nil {
		return result, nil
	}
	var request struct {
		Host struct{ ForceModelPrefix bool }
	}
	if json.Unmarshal(raw, &request) != nil || request.Host.ForceModelPrefix {
		return nil, fail(400, "search_prefix_conflict", "same-account search requires force-model-prefix to be false")
	}
	storage, _ := native["StorageJSON"].([]byte)
	c, err := parseCredential(storage)
	if err != nil {
		return nil, err
	}
	native["Prefix"] = searchAccountPrefix(c.AccountID)
	return result, nil
}

func searchSessionIDs(headers http.Header, metadata map[string]any, payload []byte) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" && len(value) <= 512 && !seen[value] {
			ids = append(ids, value)
			seen[value] = true
		}
	}
	for _, name := range []string{"Session_id", "X-Session-ID", "X-Conversation-ID"} {
		add(headers.Get(name))
	}
	for _, name := range []string{"canonical_session_id", "execution_session_id"} {
		add(stringValue(metadata[name]))
	}
	var turn struct {
		SessionID string `json:"session_id"`
		ThreadID  string `json:"thread_id"`
	}
	if json.Unmarshal([]byte(headers.Get("X-Codex-Turn-Metadata")), &turn) == nil {
		add(turn.SessionID)
		add(turn.ThreadID)
	}
	var body struct {
		ID             string `json:"id"`
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if json.Unmarshal(payload, &body) == nil {
		add(body.ID)
		add(body.PromptCacheKey)
	}
	return ids
}

func searchBindingKey(session, model string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + model))
	return hex.EncodeToString(sum[:])
}

func (b *searchAccountBindings) loadLocked(dir string) {
	if b.loaded {
		return
	}
	b.loaded = true
	b.entries = make(map[string]searchAccountBinding)
	if dir == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, "alpha-search-bindings.json"))
	if err != nil || len(raw) > 2<<20 {
		return
	}
	var entries map[string]searchAccountBinding
	if json.Unmarshal(raw, &entries) != nil || len(entries) > maxSearchBindings {
		return
	}
	for key, binding := range entries {
		if len(key) == 64 && validSearchAccountPrefix(binding.Prefix) && time.Since(binding.Seen) < searchBindingTTL {
			b.entries[key] = binding
		}
	}
}

func validSearchAccountPrefix(prefix string) bool {
	if !strings.HasPrefix(prefix, searchPrefix) {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(prefix, searchPrefix))
	return err == nil && len(b) == 16
}

func (s *Service) rememberSearchAccount(request ExecutorRequest, c credential) {
	cfg := s.config()
	if !cfg.AlphaSearchSameAccount || c.AccountID == "" {
		return
	}
	ids := searchSessionIDs(request.Headers, request.Metadata, request.OriginalRequest)
	if len(ids) == 0 {
		ids = searchSessionIDs(request.Headers, request.Metadata, request.Payload)
	}
	if len(ids) == 0 {
		return
	}
	upstream, ok := cfg.resolveUpstreamModel(request.Model)
	if !ok {
		return
	}
	b := &s.searchAccounts
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loadLocked(cfg.DataDir)
	now := time.Now()
	for key, binding := range b.entries {
		if now.Sub(binding.Seen) >= searchBindingTTL {
			delete(b.entries, key)
		}
	}
	for _, alias := range cfg.Models {
		model, _ := cfg.upstreamModelForAlias(alias)
		if model != upstream {
			continue
		}
		for _, id := range ids {
			b.entries[searchBindingKey(id, alias)] = searchAccountBinding{Prefix: searchAccountPrefix(c.AccountID), Seen: now}
		}
	}
	for len(b.entries) > maxSearchBindings {
		var oldest string
		var before time.Time
		for key, binding := range b.entries {
			if oldest == "" || binding.Seen.Before(before) {
				oldest, before = key, binding.Seen
			}
		}
		delete(b.entries, oldest)
	}
	if cfg.DataDir != "" {
		path := filepath.Join(cfg.DataDir, "alpha-search-bindings.json")
		if os.WriteFile(path+".pending", jsonBytes(b.entries), 0600) == nil {
			_ = os.Rename(path+".pending", path)
		}
	}
}

func (s *Service) routeAlphaSearch(raw json.RawMessage) (any, error) {
	var request struct {
		SourceFormat, RequestedModel string
		Headers                      http.Header
		Metadata                     map[string]any
		Body                         []byte
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fail(400, "invalid_request", "invalid Alpha Search route")
	}
	cfg := s.config()
	if !cfg.AlphaSearchSameAccount || request.SourceFormat != "codex-alpha-search" {
		return map[string]any{"Handled": false}, nil
	}
	upstream, owned := cfg.upstreamModelForAlias(request.RequestedModel)
	if !owned {
		return map[string]any{"Handled": false}, nil
	}
	// An unbound or expired session must never fall through to another account.
	prefix := searchPrefix + "unbound"
	b := &s.searchAccounts
	b.mu.Lock()
	b.loadLocked(cfg.DataDir)
	var latest searchAccountBinding
	for _, id := range searchSessionIDs(request.Headers, request.Metadata, request.Body) {
		if binding, ok := b.entries[searchBindingKey(id, request.RequestedModel)]; ok && time.Since(binding.Seen) < searchBindingTTL && binding.Seen.After(latest.Seen) {
			latest = binding
		}
	}
	if latest.Prefix != "" {
		prefix = latest.Prefix
	}
	b.mu.Unlock()
	return map[string]any{"Handled": true, "TargetKind": "provider", "Target": "codex", "TargetModel": prefix + "/" + upstream, "Reason": "Basis Points same-account Alpha Search"}, nil
}
