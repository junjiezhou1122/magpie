package provider

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// The shapes below are openrouter.ai's own, as it answered on 2026-10-06
// (evidence/live-openrouter-probe.txt). The fields are left as they came;
// the long descriptions are cut, as nothing here reads them.

// openRouterCatalog is /api/v1/models without the Anthropic header: the
// whole catalog, 466 ids, the Anthropic models among them under their own
// `anthropic/` author, nothing paged.
const openRouterCatalog = `{"data":[` +
	`{"id":"mistralai/mistral-large-4-0","name":"Mistral: Mistral Large 4","context_length":524288,` +
	`"architecture":{"input_modalities":["text","image"],"output_modalities":["text"],"modality":"text+image->text"}},` +
	`{"id":"openai/gpt-6.1-sol","name":"OpenAI: GPT-6.1 Sol","context_length":1050000,` +
	`"architecture":{"input_modalities":["file","image","text"],"output_modalities":["text"],"modality":"text+image+file->text"}},` +
	`{"id":"anthropic/claude-sonnet-5.5","name":"Anthropic: Claude Sonnet 5.5","context_length":1000000,` +
	`"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"],"modality":"text+image+file->text"}},` +
	`{"id":"anthropic/claude-sonnet-5.5:batch","name":"Anthropic: Claude Sonnet 5.5 (batch)","context_length":1000000,` +
	`"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"],"modality":"text+image+file->text"}},` +
	`{"id":"anthropic/claude-opus-5","name":"Anthropic: Claude Opus 5","context_length":1000000,` +
	`"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"],"modality":"text+image+file->text"}}` +
	`],"total_count":466,"links":{"next":null}}`

// openRouterNamespacedPage is /api/v1/models as it answers the Anthropic
// version header: the catalog again, twenty newest first, every id with
// `anthropic/` in front of it, and the cursors of the rest. None of those
// twenty is an id in the catalog above (evidence/
// live-openrouter-routability.txt): `anthropic/mistralai/mistral-large-4-0`
// reads as Anthropic's own model of Mistral's slug, and
// `openai/gpt-6.1-sol[1m]` is not an id OpenRouter serves at all.
const openRouterNamespacedPage = `{"data":[` +
	`{"id":"anthropic/mistralai/mistral-large-4-0","type":"model","display_name":"Mistral: Mistral Large 4","max_input_tokens":524288,"max_tokens":262144,"capabilities":null},` +
	`{"id":"anthropic/openai/gpt-6.1-sol","type":"model","display_name":"OpenAI: GPT-6.1 Sol","max_input_tokens":1050000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/openai/gpt-6.1-sol[1m]","type":"model","display_name":"OpenAI: GPT-6.1 Sol","max_input_tokens":1050000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/claude-sonnet-5.5","type":"model","display_name":"Anthropic: Claude Sonnet 5.5","max_input_tokens":1000000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/claude-sonnet-5.5[1m]","type":"model","display_name":"Anthropic: Claude Sonnet 5.5","max_input_tokens":1000000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/claude-sonnet-5.5:batch[1m]","type":"model","display_name":"Anthropic: Claude Sonnet 5.5 (batch)","max_input_tokens":1000000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/claude-opus-5","type":"model","display_name":"Anthropic: Claude Opus 5","max_input_tokens":1000000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/typesafe/jev-router[1m]","type":"model","display_name":"TypeSafe: Jev Router","max_input_tokens":1000000,"max_tokens":null,"capabilities":null},` +
	`{"id":"anthropic/cohere/command-a-plus","type":"model","display_name":"Cohere: Command A+","max_input_tokens":128000,"max_tokens":64000,"capabilities":null}` +
	`],"has_more":true,"first_id":"anthropic/mistralai/mistral-large-4-0",` +
	`"last_id":"anthropic/cohere/command-a-plus"}`

// oneHome gives a test a home of its own, so nothing it fetches is read
// from or written to the machine's own magpie.
func oneHome(t *testing.T) {
	t.Helper()
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
}

// A list that says it has more is a page of the vendor's catalog, not the
// catalog: a model the user picked that lives on a later page is not in
// this reply, and under the rule that a base which answers without a model
// has dropped it (#904) the pick would go with it, for good and with no way
// back. So a paged reply says nothing about what that base serves today:
// the base is treated as one that could not be asked, which keeps what it
// listed last time.
//
// Any /v1/models that paginates the way the Anthropic and OpenAI lists do
// is in this — the shape, not the vendor. OpenRouter is what was observed
// serving one; the check reads has_more and no host.
func TestFetchKeepsPicksWhenAModelListIsPaginated(t *testing.T) {
	oneHome(t)
	var paged atomic.Bool
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"anthropic/claude-opus-5"}]}`))
	}))
	defer chat.Close()
	// the whole list to begin with, then a page of it: claude-opus-5 is on
	// neither, so a reply read as the whole list drops it
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if paged.Load() {
			w.Write([]byte(openRouterNamespacedPage))
			return
		}
		w.Write([]byte(`{"data":[{"id":"anthropic/claude-opus-5"},{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{ID: "paged", Name: "Paged", Key: "sk-p",
		Chat: chat.URL + "/v1", Anthropic: anthropic.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	fetch := func() ([]string, []string) {
		p, err := Find("paged")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
		p, _ = Find("paged")
		live, _, ok := p.live()
		if !ok {
			t.Fatal("no list kept")
		}
		return idsOf(live), p.Models
	}

	got, _ := fetch()
	if want := []string{"gpt-a", "anthropic/claude-opus-5", "claude-b"}; !slices.Equal(got, want) {
		t.Fatalf("list after the first round: %v, want %v", got, want)
	}
	p, _ := Find("paged")
	p.Models = []string{"gpt-a", "anthropic/claude-opus-5", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// the base answers with a page of its list, which leaves out both of the
	// models it listed. A page is not an answer, so neither is dropped.
	paged.Store(true)
	got, picks := fetch()
	if want := []string{"gpt-a", "anthropic/claude-opus-5", "claude-b"}; !slices.Equal(got, want) {
		t.Fatalf("list after the base answered with a page: %v, want %v", got, want)
	}
	if want := []string{"gpt-a", "anthropic/claude-opus-5", "claude-b"}; !slices.Equal(picks, want) {
		t.Fatalf("picks after the base answered with a page: %v, want %v", picks, want)
	}
}

// OpenRouter's Anthropic base is not asked for the model list at all.
//
// With the Anthropic version header its /api/v1/models answers with the
// catalog again, twenty newest first, every id with `anthropic/` in front
// of the vendor's own (`anthropic/mistralai/mistral-large-4-0`) or with a
// `[1m]` suffix OpenRouter does not serve, and the cursors of the pages
// after (2026-10-06, evidence/live-openrouter-probe.txt). Merged into the
// union those twenty are twenty entries of the model picker that name no
// model: none of them is in OpenRouter's own catalog, which is the list of
// ids it serves (evidence/live-openrouter-routability.txt). That is what
// #904's reporter saw as a long, cluttered list.
//
// The rule is OpenRouter's, and is scoped to it: the same fetch for another
// vendor still merges what its Anthropic base lists, ids and all
// (TestFetchKeepsAnotherVendorsAnthropicList).
func TestFetchDoesNotMergeOpenRoutersNamespacedList(t *testing.T) {
	oneHome(t)
	asked, urls := fakeOpenRouterCatalog(t, func() string { return openRouterNamespacedPage })

	// no Preset, so the rule cannot be the preset's: it is the host the
	// user's own Chat base sits at
	if err := Save(Provider{ID: "orr", Name: "OpenRouter", Key: "k",
		Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("orr")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := idsOf(ms)
	want := []string{"mistralai/mistral-large-4-0", "openai/gpt-6.1-sol", "anthropic/claude-sonnet-5.5", "anthropic/claude-sonnet-5.5:batch", "anthropic/claude-opus-5"}
	if !slices.Equal(got, want) {
		t.Fatalf("list: %v, want the catalog alone, %v", got, want)
	}
	// the models of the other vendor, the same list by another name, are
	// not in it under a second namespace
	for _, m := range ms {
		if rest, ok := strings.CutPrefix(m.ID, "anthropic/"); ok && !slices.Contains(want, m.ID) {
			t.Errorf("namespaced id in the list: %s (the catalog has %s)", m.ID, rest)
		}
	}
	if n := asked(); n != 0 {
		t.Errorf("asked the Anthropic base %d times, want none", n)
	}
	// the fetch is one request, where before this it asked the Anthropic
	// base as well and merged a page of the catalog this one is
	if n := urls(); n != 1 {
		t.Errorf("asked %d URLs, want the Chat base's one", n)
	}
	t.Logf("asked %d URLs, %d of them with the Anthropic header", urls(), asked())
}

// The same fetch for a vendor of another kind: the union is the two bases'
// lists, and an `anthropic/` id of its own is kept. The rule above is
// OpenRouter's alone.
func TestFetchKeepsAnotherVendorsAnthropicList(t *testing.T) {
	oneHome(t)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"},{"id":"anthropic/claude-c"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{ID: "other", Name: "Other", Key: "k",
		Chat: chat.URL + "/v1", Anthropic: anthropic.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("other")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if want := []string{"gpt-a", "claude-b", "anthropic/claude-c"}; !slices.Equal(idsOf(ms), want) {
		t.Fatalf("list: %v, want %v", idsOf(ms), want)
	}
}

// One OpenRouter provider, over two rounds, with the shapes openrouter.ai
// really answered (#904, yetone's review of #1006): the catalog at the Chat
// base, and at the Anthropic base first the whole namespaced catalog and
// then one page of it. Neither the ids of that page nor a pick that fell
// off it belong to what the provider serves.
func TestFetchOpenRouterAddsNoNamespacedModelAndKeepsPicks(t *testing.T) {
	oneHome(t)
	var paged atomic.Bool
	asked, _ := fakeOpenRouterCatalog(t, func() string {
		if paged.Load() {
			return openRouterNamespacedPage
		}
		// the whole namespaced catalog: every id with `anthropic/` in front
		return strings.ReplaceAll(openRouterNamespacedPage, `"has_more":true,`, `"has_more":false,`)
	})

	if err := Save(Provider{ID: "orr2", Name: "OpenRouter", Key: "k",
		Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("orr2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	p, _ = Find("orr2")
	// a model of the catalog the user picked, which the next list has too
	p.Models = []string{"anthropic/claude-opus-5"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	paged.Store(true)
	p, _ = Find("orr2")
	ms, dropped, err := p.Refetch(context.Background())
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped %v, want none", dropped)
	}
	p, _ = Find("orr2")
	if !slices.Equal(p.Models, []string{"anthropic/claude-opus-5"}) {
		t.Errorf("picks: %v, want the one that was picked", p.Models)
	}
	for _, m := range ms {
		if rest, ok := strings.CutPrefix(m.ID, "anthropic/"); ok && rest != "" &&
			!slices.Contains([]string{"claude-sonnet-5.5", "claude-sonnet-5.5:batch", "claude-opus-5"}, rest) {
			t.Errorf("namespaced id in the list: %s", m.ID)
		}
	}
	if n := asked(); n != 0 {
		t.Errorf("asked the Anthropic base %d times, want none", n)
	}
}

// fakeOpenRouterCatalog answers for openrouter.ai and counts the requests
// that carried the Anthropic version header — the ones whose reply is the
// namespaced catalog — and every URL asked, which is what a fetch of one
// OpenRouter provider costs. namespaced says what to answer with them; the
// other replies are the catalog itself, and the URLs FetchAt would fall
// back to answer as openrouter.ai did: 404, then its web page.
func fakeOpenRouterCatalog(t *testing.T, namespaced func() string) (anthropicAsked, urls func() int32) {
	t.Helper()
	var n, hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/api/v1/models":
			if r.Header.Get("anthropic-version") != "" {
				n.Add(1)
				w.Write([]byte(namespaced()))
				return
			}
			w.Write([]byte(openRouterCatalog))
		case "/api/models":
			w.Write([]byte(`{"error":{"message":"Not Found","code":404}}`))
		default:
			w.Write([]byte("<!DOCTYPE html><html lang=\"en-US\"></html>"))
		}
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = tr
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	return n.Load, hits.Load
}
