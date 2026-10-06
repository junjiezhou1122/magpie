package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/catalog"
)

// A provider whose protocols are served at bases of their own is asked
// each of them, and the lists are merged (#904): a vendor that lists its
// Claude models at its Anthropic base and its GPT models at its Chat base
// kept only the first list that answered, so the models of every other
// endpoint were invisible in the product — in the agents' model lists, in
// the saved preferences, and even behind a model's fixed protocol, which
// setModelAPI refuses for a model the catalog does not list.
func TestFetchUnionsProtocolLists(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var chatHits, anthropicHits atomic.Int32
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		chatHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a","display_name":"GPT A",` +
			`"supported_reasoning_levels":[{"effort":"high"}],` +
			`"modalities":{"input":["text"]}}]}`))
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		anthropicHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"claude-b","display_name":"Claude B"}]}`))
	}))
	defer anthropic.Close()

	p := Provider{
		ID:        "dual",
		Name:      "Dual",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-d",
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	q, err := Find("dual")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := map[string]catalog.Model{}
	for _, m := range ms {
		got[m.ID] = m
	}
	if len(got) != 2 || got["gpt-a"].ID == "" || got["claude-b"].ID == "" {
		t.Fatalf("listed %v, want gpt-a and claude-b together", ms)
	}
	if chatHits.Load() != 1 || anthropicHits.Load() != 1 {
		t.Errorf("asked chat %d times, anthropic %d", chatHits.Load(), anthropicHits.Load())
	}
	// the list a model came from keeps its own fields
	if g := got["gpt-a"]; g.Name != "GPT A" || len(g.Efforts) != 1 || g.Efforts[0] != "high" {
		t.Errorf("gpt-a: %+v", g)
	}
	if g := got["claude-b"]; g.Name != "Claude B" {
		t.Errorf("claude-b: %+v", g)
	}
	// the models an agent is offered, and a model's own protocol setting
	// (serves, then SetModelAPI), both read the catalog the fetch kept:
	// a model only the second endpoint lists has to be in it
	avail := map[string]bool{}
	for _, m := range q.Available() {
		avail[m.ID] = true
	}
	if !avail["gpt-a"] || !avail["claude-b"] {
		t.Errorf("available %v, want gpt-a and claude-b", q.Available())
	}
	if !q.serves("claude-b") {
		t.Error("claude-b is not a model of the provider")
	}
	// the base kept for routing is the one the first protocol answered at
	if l, err := q.fetchOne(context.Background()); err != nil || l.base != chat.URL+"/v1" {
		t.Errorf("base %q (%v), want the chat one", l.base, err)
	}
}

// The order the protocols are asked in must not decide which models are
// kept: a provider with a Responses base and an Anthropic base and no Chat
// one is asked both.
func TestFetchUnionsProtocolListsWithoutChat(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var responsesHits, anthropicHits atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		responsesHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer responses.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		anthropicHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "dual2",
		Name:      "Dual 2",
		Responses: responses.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-d",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("dual2")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 2 || ms[0].ID != "gpt-a" || ms[1].ID != "claude-b" {
		t.Fatalf("listed %v, want gpt-a then claude-b", ms)
	}
	if responsesHits.Load() != 1 || anthropicHits.Load() != 1 {
		t.Errorf("asked responses %d times, anthropic %d", responsesHits.Load(), anthropicHits.Load())
	}
}

// One model in two lists is one model: its image capability is merged the
// way the keys' lists are (an explicit text-only answer wins), not taken
// from whichever list was read last.
func TestFetchUnionsSharedModel(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a","display_name":"GPT A",` +
			`"supported_reasoning_levels":[{"effort":"high"}],` +
			`"modalities":{"input":["text"]}}]}`))
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		// the same model, as the Anthropic endpoint answers for it
		w.Write([]byte(`{"data":[{"id":"gpt-a","display_name":"GPT A",` +
			`"supported_reasoning_levels":[{"effort":"high"}],` +
			`"modalities":{"input":["text","image"]}}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "shared",
		Name:      "Shared",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-d",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("shared")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("listed %v, want one model", ms)
	}
	m := ms[0]
	if m.ID != "gpt-a" || m.Name != "GPT A" || len(m.Efforts) != 1 || m.Efforts[0] != "high" {
		t.Errorf("fields lost: %+v", m)
	}
	if m.ImageInput == nil || *m.ImageInput {
		t.Errorf("image input %v, want the explicit text-only answer kept", m.ImageInput)
	}
	if m.Images {
		t.Errorf("images on, want off: %+v", m)
	}
}

// A provider whose every protocol answers keeps asking only the bases it
// speaks: one protocol means the one request it made before.
func TestFetchAsksOneProtocolOnce(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var hits atomic.Int32
	one := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`))
	}))
	defer one.Close()

	if err := Save(Provider{ID: "one", Name: "One", Chat: one.URL + "/v1", Key: "sk-o"}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("one")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 2 || ms[0].ID != "gpt-a" || ms[1].ID != "gpt-b" {
		t.Errorf("listed %v", ms)
	}
	if hits.Load() != 1 {
		t.Errorf("asked %d times, want once", hits.Load())
	}
}

// Chat and Responses at one base ask it once, as they did when the first
// answer ended the fetch.
func TestFetchAsksASharedBaseOnce(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var hits atomic.Int32
	shared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer shared.Close()

	if err := Save(Provider{
		ID:        "same",
		Name:      "Same",
		Chat:      shared.URL + "/v1",
		Responses: shared.URL + "/v1",
		Key:       "sk-s",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("same")
	if err != nil {
		t.Fatal(err)
	}
	if ms, err := q.Fetch(context.Background()); err != nil || len(ms) != 1 {
		t.Fatalf("%v %v", ms, err)
	}
	if hits.Load() != 1 {
		t.Errorf("asked %d times, want once", hits.Load())
	}
}

// Where no protocol answers, the error is the one it was: every base asked
// is named in it, with what each said, and the ids can still be typed in.
func TestFetchUnionsFailsWithEveryURL(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "dead",
		Name:      "Dead",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-x",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("dead")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err == nil {
		t.Fatalf("listed %v with no endpoint answering", ms)
	}
	msg := err.Error()
	if !strings.Contains(msg, chat.URL+"/v1/models: 404") || !strings.Contains(msg, anthropic.URL) {
		t.Errorf("error does not name every base asked: %s", msg)
	}
	if !strings.Contains(msg, "by hand") {
		t.Errorf("error lost the way out: %s", msg)
	}
	if ms != nil {
		t.Errorf("listed %v with no endpoint answering", ms)
	}
	if _, _, ok := catalog.Live("dead"); ok {
		t.Error("a catalog was kept for a provider that answered nothing")
	}
}

// A models URL is where the user said its list is: that URL alone is asked,
// once, and no base is.
func TestFetchModelsURLAsksNoBase(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var baseHits atomic.Int32
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baseHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer base.Close()
	var urlHits atomic.Int32
	list := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer list.Close()

	if err := Save(Provider{
		ID:        "listed",
		Name:      "Listed",
		Chat:      base.URL + "/v1",
		Anthropic: base.URL + "/v1",
		ModelsURL: list.URL + "/v1/models",
		Key:       "sk-l",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("listed")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 1 || ms[0].ID != "claude-b" {
		t.Errorf("listed %v, want the models URL's own list", ms)
	}
	if urlHits.Load() != 1 {
		t.Errorf("asked the models URL %d times, want once", urlHits.Load())
	}
	if baseHits.Load() != 0 {
		t.Errorf("asked a base %d times beside the models URL", baseHits.Load())
	}
}

// A protocol of a multi-protocol provider that can't be asked now keeps the
// models it listed last time, as a key that can't be asked does: a failed
// read says nothing about what the vendor serves today, and without it a
// picked Claude model is gone from the user's provider for good (#904,
// yetone's review of the union fetch: an Anthropic base answering 502
// deleted its models from the picks and never brought them back).
func TestFetchKeepsTheModelsAFailedProtocolListed(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	var list atomic.Value
	list.Store(`{"data":[{"id":"claude-b"}]}`)
	var down atomic.Bool
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(list.Load().(string)))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "failing",
		Name:      "Failing",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-f",
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		p, err := Find("failing")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Fetch(context.Background())
		return err
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	// both models picked from the list both protocols answered with
	p, _ := Find("failing")
	p.Models = []string{"gpt-a", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// the Anthropic base is down: the fetch still succeeds on the Chat one,
	// and nothing the failed endpoint listed goes missing
	down.Store(true)
	if err := fetch(); err != nil {
		t.Fatalf("fetch with one protocol down: %v", err)
	}
	p, _ = Find("failing")
	if want := []string{"gpt-a", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after a protocol failed: %v, want %v", p.Models, want)
	}
	live, _, ok := p.live()
	if !ok {
		t.Fatal("no list kept after a protocol failed")
	}
	if ids := idsOf(live); !slices.Equal(ids, []string{"gpt-a", "claude-b"}) {
		t.Fatalf("list after a protocol failed: %v, want gpt-a and claude-b", ids)
	}
	if !p.serves("claude-b") {
		t.Error("claude-b is not a model of the provider any more")
	}

	// and once the endpoint answers again its real list stands: a model it
	// has dropped since is dropped here, so keeping is not keeping forever
	down.Store(false)
	list.Store(`{"data":[{"id":"claude-c"}]}`)
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("failing")
	if want := []string{"gpt-a"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after the endpoint answered again: %v, want %v", p.Models, want)
	}
	live, _, ok = p.live()
	if !ok {
		t.Fatal("no list kept after the endpoint answered again")
	}
	if ids := idsOf(live); !slices.Equal(ids, []string{"gpt-a", "claude-c"}) {
		t.Fatalf("list after the endpoint answered again: %v, want gpt-a and claude-c", ids)
	}
}

// The other way round, which is what stops the fix from keeping every old
// model: a model only the Chat base listed is gone from that base's list,
// and the Anthropic base failing says nothing about it. Keeping every model
// of a failed fetch would resurrect it (yetone's review).
func TestFetchDropsAGoneModelWhileAnotherProtocolFails(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var list atomic.Value
	list.Store(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(list.Load().(string)))
	}))
	defer chat.Close()
	var down atomic.Bool
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "half",
		Name:      "Half",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-h",
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		p, err := Find("half")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Fetch(context.Background())
		return err
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("half")
	p.Models = []string{"gpt-a", "gpt-b", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// the Chat base has dropped gpt-b, and the Anthropic base is down
	list.Store(`{"data":[{"id":"gpt-a"}]}`)
	down.Store(true)
	if err := fetch(); err != nil {
		t.Fatalf("fetch with one protocol down: %v", err)
	}
	p, _ = Find("half")
	if want := []string{"gpt-a", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after the Chat list dropped a model: %v, want %v", p.Models, want)
	}
	live, _, ok := p.live()
	if !ok {
		t.Fatal("no list kept after a protocol failed")
	}
	if ids := idsOf(live); !slices.Equal(ids, []string{"gpt-a", "claude-b"}) {
		t.Fatalf("list after the Chat list dropped a model: %v, want gpt-a and claude-b", ids)
	}
}

// idsOf are the ids of a list, in its order.
func idsOf(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// The same where each of several keys has a list of its own: a base of the
// provider's that can't be asked keeps its models, and every model's keys
// are its own still.
func TestFetchKeepsTheModelsAFailedProtocolListedPerKey(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	var down atomic.Bool
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "keys",
		Name:      "Keys",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-1",
		Keys:      []KeyAccount{{Key: "sk-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		p, err := Find("keys")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Fetch(context.Background())
		return err
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("keys")
	p.Models = []string{"gpt-a", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	down.Store(true)
	if err := fetch(); err != nil {
		t.Fatalf("fetch with one protocol down: %v", err)
	}
	p, _ = Find("keys")
	if want := []string{"gpt-a", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after a protocol failed: %v, want %v", p.Models, want)
	}
	live, _, ok := p.live()
	if !ok {
		t.Fatal("no list kept after a protocol failed")
	}
	if ids := idsOf(live); !slices.Equal(ids, []string{"gpt-a", "claude-b"}) {
		t.Fatalf("list after a protocol failed: %v, want gpt-a and claude-b", ids)
	}
	// both keys still see both models: a model kept for a base that failed
	// is not a model of the keys that answered beside it alone
	for _, m := range live {
		if len(m.Keys) != 2 || !slices.Contains(m.Keys, KeyID("sk-1")) || !slices.Contains(m.Keys, KeyID("sk-2")) {
			t.Errorf("%s keys %v, want both", m.ID, m.Keys)
		}
	}
}

// A list saved before the parts were kept says nothing about which base
// listed what, so the models no answering base lists are kept this once, and
// the next fetch with every base answering settles it: what that base does
// not list is gone, however long it was kept for.
func TestFetchKeepsModelsOfAListSavedWithoutParts(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	var down atomic.Bool
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "unrecorded",
		Name:      "Unrecorded",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-u",
	}); err != nil {
		t.Fatal(err)
	}
	// the list a fetch before this change kept: the union, with no part
	// saying which base listed what
	if err := catalog.SaveLive("unrecorded", chat.URL+"/v1", []catalog.Model{
		{ID: "gpt-a"}, {ID: "gpt-b"}, {ID: "claude-b"},
	}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("unrecorded")
	p.Models = []string{"gpt-a", "gpt-b", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	down.Store(true)
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch with one protocol down: %v", err)
	}
	p, _ = Find("unrecorded")
	if want := []string{"gpt-a", "gpt-b", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after a protocol failed: %v, want %v", p.Models, want)
	}

	// every base answering now says which models are its own: gpt-b, gone
	// from the Chat list, is gone
	down.Store(false)
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("unrecorded")
	if want := []string{"gpt-a", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after every base answered: %v, want %v", p.Models, want)
	}
}
