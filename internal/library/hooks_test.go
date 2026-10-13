package library

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
)

// hooksOf is the list a hooks file has under hooks.<ev>, as JSON.
func hooksOf(t *testing.T, path, ev string) []string {
	t.Helper()
	var out []string
	for _, e := range gjson.GetBytes(jsonc.ToJSON([]byte(read(t, path))), "hooks."+ev).Array() {
		out = append(out, canonical([]byte(e.Raw)))
	}
	return out
}

// The two of #1790: Bark when a Codex turn ends, a formatter after Claude
// Code edits a file. Each goes into its agent's file in its own shape,
// beside the user's own hooks, and comes out of it alone: the user's
// hooks, their comments and their other settings stay as they were.
func TestHooksEachAgentsOwnShape(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	mine := `{
  // the user's own
  "model": "opus",
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "say done"}]}],
    "PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "my-log"}]}]
  }
}
`
	write(t, settings, mine)
	codexHooks := filepath.Join(h, ".codex/hooks.json")

	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "curl -s https://api.day.app/KEY/done", Timeout: 10,
		Agents: []string{"codex", "gemini", "cursor"}}))
	ok(t)(SaveHook("", Hook{Name: "fmt", Event: "after-tool", Matcher: "edit", Command: "prettier --write .", Agents: []string{"claude", "codex", "gemini", "cursor"}}))

	if got := hooksOf(t, codexHooks, "Stop"); len(got) != 1 || got[0] != `{"hooks":[{"command":"curl -s https://api.day.app/KEY/done","timeout":10,"type":"command"}]}` {
		t.Errorf("codex Stop: %v", got)
	}
	if got := hooksOf(t, codexHooks, "PostToolUse"); len(got) != 1 || got[0] != `{"hooks":[{"command":"prettier --write .","type":"command"}],"matcher":"apply_patch"}` {
		t.Errorf("codex PostToolUse: %v", got)
	}
	if got := hooksOf(t, settings, "PostToolUse"); len(got) != 2 || got[1] != `{"hooks":[{"command":"prettier --write .","type":"command"}],"matcher":"Edit|Write|MultiEdit"}` {
		t.Errorf("claude PostToolUse, the user's first: %v", got)
	}
	if got := hooksOf(t, settings, "Stop"); len(got) != 1 {
		t.Errorf("claude was given bark, which isn't ticked for it: %v", got)
	}
	gem := filepath.Join(h, ".gemini/settings.json")
	if got := hooksOf(t, gem, "AfterAgent"); len(got) != 1 || !strings.Contains(got[0], `"timeout":10000`) {
		t.Errorf("gemini's timeout is in ms: %v", got)
	}
	if got := hooksOf(t, gem, "AfterTool"); len(got) != 1 || !strings.Contains(got[0], `"matcher":"write_file|replace"`) {
		t.Errorf("gemini AfterTool: %v", got)
	}
	cur := filepath.Join(h, ".cursor/hooks.json")
	if got := hooksOf(t, cur, "stop"); len(got) != 1 || got[0] != `{"command":"curl -s https://api.day.app/KEY/done","timeout":10}` {
		t.Errorf("cursor's entry is the command itself: %v", got)
	}
	if v := gjson.Get(read(t, cur), "version").Int(); v != 1 {
		t.Errorf("cursor's hooks.json has version %d", v)
	}

	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Hooks) != 2 || v.Hooks[0].Name != "bark" || v.Hooks[0].Native["codex"] != "Stop" || v.Hooks[0].Native["gemini"] != "AfterAgent" {
		t.Errorf("view: %+v", v.Hooks)
	}

	ok(t)(RemoveHooks([]string{"bark", "fmt"}))
	// the same settings, its comment kept (a list magpie wrote into is
	// spaced the way the editor spaces one)
	if got := read(t, settings); canonical(jsonc.ToJSON([]byte(got))) != canonical(jsonc.ToJSON([]byte(mine))) || !strings.Contains(got, "// the user's own") {
		t.Errorf("claude's settings after, not as the user had them:\n%s", got)
	}
	if got := gjson.Get(read(t, codexHooks), "hooks"); got.Exists() {
		t.Errorf("codex's hooks left: %s", got.Raw)
	}
	if got := gjson.Get(read(t, gem), "hooks"); got.Exists() {
		t.Errorf("gemini's hooks left: %s", got.Raw)
	}
	if v, _ := Read(nil); len(v.Hooks) != 0 {
		t.Errorf("left in the library: %+v", v.Hooks)
	}
}

// An entry the user changed after magpie wrote it is theirs: taking the
// hook out of the library, or changing it, leaves their entry be. One the
// user had already, the same as the library's, is neither written twice
// nor taken.
func TestHooksTheUsersEditsAreTheirs(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	write(t, settings, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"bark"}]}]}}`)
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "bark", Agents: []string{"claude"}}))
	if got := hooksOf(t, settings, "Stop"); len(got) != 1 {
		t.Fatalf("the user's own was written again: %v", got)
	}
	ok(t)(RemoveHooks([]string{"bark"}))
	if got := hooksOf(t, settings, "Stop"); len(got) != 1 {
		t.Fatalf("the user's own was taken out: %v", got)
	}

	ok(t)(SaveHook("", Hook{Name: "fmt", Event: "after-tool", Matcher: "edit", Command: "gofmt -w .", Agents: []string{"claude"}}))
	s := read(t, settings)
	write(t, settings, strings.Replace(s, "gofmt -w .", "gofmt -w ./cmd", 1))
	ok(t)(HookAgents("fmt", []string{}))
	if got := hooksOf(t, settings, "PostToolUse"); len(got) != 1 || !strings.Contains(got[0], "./cmd") {
		t.Errorf("the user's edit of magpie's entry was taken out: %v", got)
	}

	// a changed hook goes out as it was and in as it is
	ok(t)(SaveHook("", Hook{Name: "lint", Event: "before-tool", Matcher: "shell", Command: "lint-a", Agents: []string{"claude"}}))
	ok(t)(SaveHook("lint", Hook{Name: "lint", Event: "before-tool", Matcher: "shell", Command: "lint-b", Agents: []string{"claude"}}))
	if got := hooksOf(t, settings, "PreToolUse"); len(got) != 1 || !strings.Contains(got[0], "lint-b") || !strings.Contains(got[0], `"matcher":"Bash"`) {
		t.Errorf("claude PreToolUse after the change: %v", got)
	}
}

// A file that doesn't parse is unknown, never empty: it is left as it is,
// and the hook said not to be given.
func TestHooksUnreadableFileLeftAlone(t *testing.T) {
	h := sandbox(t)
	codexHooks := filepath.Join(h, ".codex/hooks.json")
	bad := `{"hooks": {"Stop": [ oops`
	write(t, codexHooks, bad)
	res, err := SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "bark", Agents: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, codexHooks) != bad {
		t.Error("a hooks.json magpie can't read was written over")
	}
	if !slices.ContainsFunc(res.Problems, func(p Problem) bool { return p.Agent == "codex" && p.What == "hook:bark" }) {
		t.Errorf("problems: %+v", res.Problems)
	}
	if a := mustLoad(t).Applied["codex"]; a != nil && len(a.Hooks) != 0 {
		t.Errorf("recorded as written: %+v", a.Hooks)
	}
}

// Codex trusts a hook by its place in hooks.json: a group of magpie's
// taken out from before the user's own moves the user's trust with it,
// and the record of the group taken out goes.
func TestHooksCodexTrustMovesWithTheGroups(t *testing.T) {
	h := sandbox(t)
	codexHooks := filepath.Join(h, ".codex/hooks.json")
	cfg := filepath.Join(h, ".codex/config.toml")
	write(t, cfg, "model = \"gpt-5\"\n")
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "bark", Agents: []string{"codex"}}))
	// the user adds one of their own after it and trusts both in /hooks
	raw := read(t, codexHooks)
	var doc map[string]map[string][]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	doc["hooks"]["Stop"] = append(doc["hooks"]["Stop"], json.RawMessage(`{"hooks":[{"type":"command","command":"mine"}]}`))
	b, _ := json.Marshal(doc)
	write(t, codexHooks, string(b))
	key := func(g int) string { return codexHooks + ":stop:" + string(rune('0'+g)) + ":0" }
	write(t, cfg, read(t, cfg)+"\n[hooks.state.\""+key(0)+"\"]\ntrusted_hash = \"sha256:aaa\"\n\n[hooks.state.\""+key(1)+"\"]\ntrusted_hash = \"sha256:bbb\"\n\n[hooks.state.\""+codexHooks+":pre_tool_use:0:0\"]\nenabled = false\n")

	ok(t)(RemoveHooks([]string{"bark"}))
	if got := hooksOf(t, codexHooks, "Stop"); len(got) != 1 || !strings.Contains(got[0], "mine") {
		t.Fatalf("codex Stop after: %v", got)
	}
	c := read(t, cfg)
	want := "model = \"gpt-5\"\n\n[hooks.state.\"" + key(0) + "\"]\ntrusted_hash = \"sha256:bbb\"\n\n[hooks.state.\"" + codexHooks + ":pre_tool_use:0:0\"]\nenabled = false\n"
	if c != want {
		t.Errorf("config.toml after:\n%s\nwant:\n%s", c, want)
	}
}

// An agent's own event goes to that agent alone, and an agent magpie can't
// give hooks to is said to be one, on the page, before any change.
func TestHooksNativeEventsAndUnavailableAgents(t *testing.T) {
	h := sandbox(t)
	if _, err := SaveHook("", Hook{Name: "ask", Event: "codex:PermissionRequest", Command: "x", Agents: []string{"claude"}}); err == nil {
		t.Error("codex's own event was given to claude")
	}
	if _, err := SaveHook("", Hook{Name: "bad", Event: "turn-end", Matcher: "shell", Command: "x"}); err == nil {
		t.Error("turn-end was narrowed to the shell")
	}
	if _, err := SaveHook("", Hook{Name: "bad", Event: "after-tool", Command: " "}); err == nil {
		t.Error("a hook with no command was kept")
	}
	ok(t)(SaveHook("", Hook{Name: "ask", Event: "codex:PermissionRequest", Matcher: "Bash", Command: "notify", Agents: []string{"codex"}}))
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "bark"}))
	ok(t)(SomeHooksAgents([]string{"ask", "bark"}, []string{"claude", "opencode"}, true))
	l := mustLoad(t)
	if a := l.hook("ask").Agents; !slices.Equal(a, []string{"codex"}) {
		t.Errorf("ask's agents: %v", a)
	}
	// many at once go to the agents that can take them; one ticked by
	// itself that can't is said why
	if a := l.hook("bark").Agents; !slices.Equal(a, []string{"claude"}) {
		t.Errorf("bark's agents: %v", a)
	}
	if _, err := HookAgents("bark", []string{"claude", "opencode"}); err != nil {
		t.Fatal(err)
	}
	if got := hooksOf(t, filepath.Join(h, ".codex/hooks.json"), "PermissionRequest"); len(got) != 1 || !strings.Contains(got[0], `"matcher":"Bash"`) {
		t.Errorf("codex PermissionRequest: %v", got)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(v.Hooks, func(x HookView) bool { return x.Name == "bark" })
	if i < 0 || v.Hooks[i].Problems["opencode"] == "" || v.Hooks[i].Problems["claude"] != "" {
		t.Errorf("bark's problems: %+v", v.Hooks)
	}
	j := slices.IndexFunc(v.HookAgents, func(a HookAgentView) bool { return a.ID == "codex" })
	if j < 0 || !slices.Contains(v.HookAgents[j].Events, "PermissionRequest") || v.HookAgents[j].File == "" {
		t.Errorf("codex's view: %+v", v.HookAgents)
	}
}

// Hooks turned off in the agent are said: Claude Code's disableAllHooks.
func TestHooksOffSaid(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".claude/settings.json"), `{"disableAllHooks": true}`)
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	j := slices.IndexFunc(v.HookAgents, func(a HookAgentView) bool { return a.ID == "claude" })
	if j < 0 || !v.HookAgents[j].Off {
		t.Errorf("claude's view: %+v", v.HookAgents)
	}
}
