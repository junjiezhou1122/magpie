package provider

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// OpenCode Zen's free models are listed: magpie asks them as OpenCode does
// (gateway/zenfree.go), so they answer.
func TestOpenCodeZenListsFreeModels(t *testing.T) {
	p, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	got := p.planModels([]catalog.Model{{ID: "mimo-v2.6-flash-free"}, {ID: "claude-opus-5-5"}, {ID: "gpt-5.5"}})
	if len(got) != 3 || got[0].ID != "mimo-v2.6-flash-free" {
		t.Fatalf("got %v", got)
	}
	if !p.OpenCodeFree("mimo-v2.6-flash-free") || p.OpenCodeFree("gpt-5.5") {
		t.Fatal("OpenCodeFree")
	}
	if (Provider{Preset: "together"}).OpenCodeFree("a-free") {
		t.Fatal("OpenCodeFree on another provider")
	}
}

// The Test button's probe of a free model is streamed and offers bash and
// read, each in the protocol's own tool shape.
func TestZenFreeProbe(t *testing.T) {
	for proto, key := range map[Protocol]string{Chat: "function", Responses: "name", Anthropic: "input_schema"} {
		var m map[string]any
		if err := json.Unmarshal([]byte(zenFreeProbe(proto, `{"model":"x-free","max_tokens":1}`)), &m); err != nil {
			t.Fatal(err)
		}
		tools, _ := m["tools"].([]any)
		if m["stream"] != true || len(tools) != 2 || m["model"] != "x-free" {
			t.Fatalf("%v: %v", proto, m)
		}
		if _, ok := tools[0].(map[string]any)[key]; !ok {
			t.Fatalf("%v: tool %v", proto, tools[0])
		}
	}
}

// #572: an OpenCode Zen provider added with no key asks with the one
// OpenCode sends signed out, which Zen's free models answer; a key of the
// user's own is kept.
func TestOpenCodeZenNeedsNoKey(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	p, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"mimo-v2.6-flash-free"}
	id, err := Add(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != OpenCodeAnonymousKey {
		t.Fatalf("key %q", got.Key)
	}
	p.Name, p.Key = "Zen paid", "sk-own"
	if id, err = Add(p); err != nil {
		t.Fatal(err)
	}
	if got, _ = Find(id); got.Key != "sk-own" {
		t.Fatalf("own key %q", got.Key)
	}
	// another provider still needs one
	if _, err := Add(Provider{Name: "Relay", Chat: "https://api.relay.example/v1", Models: []string{"m"}}); err == nil {
		t.Fatal("a relay with no key")
	}
}

// #1795: Add another key on an OpenCode Zen provider with a key of the
// user's own, with nothing pasted, adds public behind it, as a new Zen
// provider saved with no key has it; asked again, it says it has it. Kilo,
// whose free models need no key too, has no such key to add.
func TestZenAddKeyEmptyIsPublic(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	zen, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	zen.ID, zen.Key = "zen", "sk-own"
	if err := Save(zen); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("zen", "", "", ""); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("zen")
	if on := p.KeysOn(); len(on) != 2 || on[0].Key != "sk-own" || on[1].Key != OpenCodeAnonymousKey {
		t.Fatalf("keys on %+v", on)
	}
	if err := AddKey("zen", "", "", ""); err == nil || !strings.Contains(err.Error(), "already has public") {
		t.Fatalf("public twice: %v", err)
	}
	kilo, err := FromPreset("kilo")
	if err != nil {
		t.Fatal(err)
	}
	kilo.ID, kilo.Key = "kilo", "sk-kilo"
	if err := Save(kilo); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("kilo", "", "", ""); err == nil {
		t.Fatal("Kilo took an empty key")
	}
}
