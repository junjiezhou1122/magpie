package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider of the user's own, at its address on the API it speaks, is
// added from the TUI as the app's custom provider editor and magpie provider
// add <name> url=… add it (hezz1891 on Discord: on a server, the TUI had
// vendors magpie knows alone).
func TestTUIAddsACustomProvider(t *testing.T) {
	home(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-relay" {
			http.Error(w, `{"error":"no"}`, http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{
			{"id": "qwen3.6-max", "object": "model"}, {"id": "glm-5.3", "object": "model"},
		}})
	}))
	defer srv.Close()

	m := press(t, model{w: 140, h: 40}, "2", "a")
	if !strings.Contains(m.View(), "or custom for your own API") {
		t.Fatalf("the vendors' line doesn't say how to add one's own:\n%s", m.View())
	}
	m = typeIn(m, "custom")
	for _, id := range []string{"custom-openai", "custom-responses", "custom-anthropic", "custom-gemini"} {
		if !strings.Contains(m.View(), id) {
			t.Fatalf("custom doesn't offer %s:\n%s", id, m.View())
		}
	}
	m = typeIn(m, "-openai")
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Placeholder != "https://api.example.com/v1" {
		t.Fatalf("custom asked %q (mode %v), not the base URL", m.ask.input.Placeholder, m.mode)
	}
	// not an address: said so, nothing asked after it
	m = typeIn(m, "api.example.com")
	m = press(t, m, "enter")
	wantFlash(t, m, false, "http:// or https://")

	m = press(t, m, "a")
	m = typeIn(m, "custom-openai")
	m = press(t, m, "enter")
	m = typeIn(m, srv.URL+"/v1/")
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Placeholder != "127.0.0.1" {
		t.Fatalf("after the URL it asked %q (mode %v), not the name", m.ask.input.Placeholder, m.mode)
	}
	m = typeIn(m, "My Relay")
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Placeholder != "the API key" {
		t.Fatalf("after the name it asked %q (mode %v), not the key", m.ask.input.Placeholder, m.mode)
	}
	m = typeIn(m, "sk-relay")
	m = press(t, m, "enter")
	wantFlash(t, m, true, "added My Relay · 2 models")
	p, err := provider.Find("my-relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != srv.URL+"/v1" || p.Responses != "" || p.Anthropic != "" || p.Key != "sk-relay" || p.Preset != "" {
		t.Fatalf("saved chat %q responses %q anthropic %q key %q preset %q", p.Chat, p.Responses, p.Anthropic, p.Key, p.Preset)
	}

	// each API sets its own URL; a name left empty is the host's. (Each
	// has a key of its own: the same host with the same key is the
	// provider already there, which provider.Add turns away. Nothing
	// listens at :1, so the list is refused at once.)
	for _, c := range []struct {
		id, name, want, url string
		got                 func(provider.Provider) string
	}{
		{"custom-responses", "", "127.0.0.1", "http://127.0.0.1:1/v1", func(p provider.Provider) string { return p.Responses }},
		{"custom-anthropic", "Claude relay", "Claude relay", "http://127.0.0.1:1", func(p provider.Provider) string { return p.Anthropic }},
		{"custom-gemini", "Gemini relay", "Gemini relay", "http://127.0.0.1:1/v1beta", func(p provider.Provider) string { return p.Gemini }},
	} {
		m = press(t, m, "a")
		m = typeIn(m, c.id)
		m = press(t, m, "enter")
		m = typeIn(m, c.url)
		m = press(t, m, "enter")
		if c.name != "" {
			m = typeIn(m, c.name)
		}
		m = press(t, m, "enter")
		m = typeIn(m, "sk-"+c.id)
		m = press(t, m, "enter")
		i := slices.IndexFunc(provider.All(), func(q provider.Provider) bool { return q.Name == c.want })
		if i < 0 {
			t.Errorf("%s: no provider named %s (flash %q)", c.id, c.want, m.flash)
			continue
		}
		q := provider.All()[i]
		if c.got(q) != c.url || q.Chat != "" {
			t.Errorf("%s: saved %q (chat %q), not %q", c.id, c.got(q), q.Chat, c.url)
		}
	}
}
