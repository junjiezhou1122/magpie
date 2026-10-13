package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// notice is the item Codex writes between one batch's tool results when the
// image a tool returned was resized for the model.
const resizeNotice = `<image_resize_notice>
Image 1 of 1 in the preceding tool output was resized from 1024x9000 to 233x2048 pixels.
</image_resize_notice>`

// orderOf is a Responses request's input items as "type:call_id" strings.
func orderOf(t *testing.T, body []byte) []string {
	t.Helper()
	var order []string
	for _, it := range inputOf(t, body) {
		typ, _ := it["type"].(string)
		id, _ := it["call_id"].(string)
		order = append(order, typ+":"+id)
	}
	return order
}

// A batch of two calls whose results have a message between them: the
// message goes after both results, where the run a strict upstream checks
// is whole. Fixtures are the reporter's own bytes — two parallel calls
// (view_image and exec_command), an output holding an image, Codex's resize
// notice, and the second output.
func TestJoinToolRunsMovesAnItemOutOfARun(t *testing.T) {
	body := []byte(`{"model":"m","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"look"}]},` +
		`{"type":"function_call","call_id":"call_00_hC0DonBjeKHawzABcyLL1642","name":"view_image","arguments":"{\"path\":\"wide.png\"}"},` +
		`{"type":"function_call","call_id":"call_01_mNySew0rARWuQYQXPeIY2319","name":"exec_command","arguments":"{\"cmd\":\"python -c ...\"}"},` +
		`{"type":"function_call_output","call_id":"call_00_hC0DonBjeKHawzABcyLL1642","output":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},` +
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":` + jsonStr(resizeNotice) + `}]},` +
		`{"type":"function_call_output","call_id":"call_01_mNySew0rARWuQYQXPeIY2319","output":"PIL 11.3.0\r\n"}]}`)
	original := bytes.Clone(body)
	got := joinToolRuns(body)
	if !bytes.Equal(body, original) {
		t.Fatal("mutated the caller's request buffer")
	}
	want := []string{
		"message:",
		"function_call:call_00_hC0DonBjeKHawzABcyLL1642",
		"function_call:call_01_mNySew0rARWuQYQXPeIY2319",
		"function_call_output:call_00_hC0DonBjeKHawzABcyLL1642",
		"function_call_output:call_01_mNySew0rARWuQYQXPeIY2319",
		"message:",
	}
	order := orderOf(t, got)
	if len(order) != len(want) {
		t.Fatalf("items %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("items %v, want %v", order, want)
		}
	}
	// the notice keeps its text, and the results keep theirs
	if !bytes.Contains(got, []byte("image_resize_notice")) ||
		!bytes.Contains(got, []byte("PIL 11.3.0")) ||
		!bytes.Contains(got, []byte("data:image/png;base64,AQID")) {
		t.Fatalf("lost an item's content: %s", got)
	}
	// the same request again is left as it is
	if again := joinToolRuns(got); !bytes.Equal(again, got) {
		t.Fatalf("not idempotent:\n%s\n%s", got, again)
	}
}

// Only an item that breaks a run moves: one between two whole exchanges, a
// request with no tool items, and anything that isn't a Responses body go
// byte for byte.
func TestJoinToolRunsLeavesWholeExchangesAlone(t *testing.T) {
	for _, keep := range []string{
		// each exchange is whole: the call before the notice is answered,
		// and so is the one after it
		`{"model":"m","input":[` +
			`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"call_a","output":"a"},` +
			`{"type":"message","role":"developer","content":[{"type":"input_text","text":"a note"}]},` +
			`{"type":"function_call","call_id":"call_b","name":"echo","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"call_b","output":"b"}]}`,
		// a call nothing answers is not a run to keep together
		`{"model":"m","input":[` +
			`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"never mind"}]}]}`,
		// parallel results already contiguous, and out of the calls' order
		`{"model":"m","input":[` +
			`{"type":"function_call","call_id":"call_a","name":"echo","arguments":"{}"},` +
			`{"type":"function_call","call_id":"call_b","name":"echo","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"call_b","output":"b"},` +
			`{"type":"function_call_output","call_id":"call_a","output":"a"}]}`,
		`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
		`not json`, `{}`, `{"input":"hello"}`,
	} {
		if got := joinToolRuns([]byte(keep)); string(got) != keep {
			t.Fatalf("rewritten:\n%s\nto\n%s", keep, got)
		}
	}
}

// The relay keeps the run whole for the upstream: a request whose batch has
// an item between its results comes out with the results together, and an
// upstream that checks the exchange answers it.
func TestResponsesRelayKeepsAToolRunTogether(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)}
	f.refuse = func(body []byte) (int, string) {
		// The upstream's own check, on the chat form it builds: every result
		// of a run must follow the calls without an item between them.
		open := 0
		var sent struct {
			Input []struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		for _, it := range sent.Input {
			switch it.Type {
			case "function_call":
				open++
			case "function_call_output":
				open--
			default:
				if open > 0 {
					return 400, `{"error":{"message":"An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'."}}`
				}
			}
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	body := `{"model":"fake/m1","stream":true,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"look"}]},` +
		`{"type":"function_call","call_id":"call_a","name":"view_image","arguments":"{}"},` +
		`{"type":"function_call","call_id":"call_b","name":"exec_command","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_a","output":"looked"},` +
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":` + jsonStr(resizeNotice) + `}]},` +
		`{"type":"function_call_output","call_id":"call_b","output":"ran"}]}`
	if code, out := post(t, "/v1/responses", body); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	want := []string{"message:", "function_call:call_a", "function_call:call_b",
		"function_call_output:call_a", "function_call_output:call_b", "message:"}
	order := orderOf(t, f.got)
	if len(order) != len(want) {
		t.Fatalf("sent %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("sent %v, want %v", order, want)
		}
	}
}

// #1822's turn, built from the reporter's rollout: Codex's two parallel
// view_image calls (lines 342/344), the first image's output (347), the
// <image_resize_notice> Codex writes right after the output whose image it
// resized (348; image_preparation.rs, prepare_response_items), and the
// second output (349). OpenCode Go (DeepSeek's /responses) answered
// "No tool output found for tool call call_01_ET_grdFcOklLqmv9lMypDJt9860"
// with the result right there. The fake checks what DeepSeek checks: each
// call of a step is answered in the run of results that follows it.
func TestRelayedCodexImageBatchKeepsItsResultsTogether(t *testing.T) {
	const a, b = "call_00_ET_KphthFDdQvApJCWnZN8m9740", "call_01_ET_grdFcOklLqmv9lMypDJt9860"
	f := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)}
	f.refuse = func(body []byte) (int, string) {
		var sent struct {
			Input []struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		var owed []string
		for _, it := range sent.Input {
			switch it.Type {
			case "function_call":
				owed = append(owed, it.CallID)
			case "function_call_output":
				owed = slices.DeleteFunc(owed, func(id string) bool { return id == it.CallID })
			default:
				if len(owed) > 0 {
					return 400, `{"error":{"message":"No tool output found for tool call ` + owed[0] + `.","type":"invalid_request_error"}}`
				}
			}
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	notice := "<image_resize_notice>\nImage 1 of 1 in the preceding tool output was resized from 2280x156 to 2048x140 pixels.\n</image_resize_notice>"
	body := `{"model":"fake/m1","stream":true,"store":false,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"看一下这两张截图"}]},` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"Look at both."}]},` +
		`{"type":"function_call","call_id":"` + a + `","name":"view_image","arguments":"{\"path\":\"C:\\\\Users\\\\NianLee\\\\a.png\"}"},` +
		`{"type":"function_call","call_id":"` + b + `","name":"view_image","arguments":"{\"path\":\"C:\\\\Users\\\\NianLee\\\\b.png\"}"},` +
		`{"type":"function_call_output","call_id":"` + a + `","output":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},` +
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":` + jsonStr(notice) + `}]},` +
		`{"type":"function_call_output","call_id":"` + b + `","output":[{"type":"input_image","image_url":"data:image/png;base64,BBBB"}]}]}`
	if code, out := post(t, "/v1/responses", body); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	want := []string{"message:", "reasoning:", "function_call:" + a, "function_call:" + b,
		"function_call_output:" + a, "function_call_output:" + b, "message:"}
	if got := orderOf(t, f.got); !slices.Equal(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	for _, kept := range []string{"AAAA", "BBBB", "2280x156 to 2048x140"} {
		if !bytes.Contains(f.got, []byte(kept)) {
			t.Fatalf("%q was lost: %s", kept, f.got)
		}
	}
}

// What moves and what doesn't, beyond the reporter's shape.
func TestJoinToolRunsOnlyMovesWhatBreaksARun(t *testing.T) {
	call := func(id string) string {
		return `{"type":"function_call","call_id":"` + id + `","name":"f","arguments":"{}"}`
	}
	res := func(id string) string {
		return `{"type":"function_call_output","call_id":"` + id + `","output":"r"}`
	}
	note := func(role, text string) string {
		return `{"type":"message","role":"` + role + `","content":[{"type":"input_text","text":"` + text + `"}]}`
	}
	const reasoning = `{"type":"reasoning","summary":[{"type":"summary_text","text":"next"}]}`
	for _, c := range []struct {
		name string
		in   []string
		want []string // "type:call_id", or the text of a message
	}{
		{"the model's own step between its calls stays",
			[]string{call("a"), reasoning, note("assistant", "and"), call("b"), res("a"), res("b")},
			[]string{"function_call:a", "reasoning:", "message:", "function_call:b", "function_call_output:a", "function_call_output:b"}},
		{"a note before any result, not the model's, goes after the run",
			[]string{call("a"), note("user", "from a subagent"), res("a")},
			[]string{"function_call:a", "function_call_output:a", "message:"}},
		{"an item with a call_id never moves, whatever its type",
			[]string{call("a"), call("b"), res("a"), `{"type":"local_shell_call","call_id":"s","action":{}}`, res("b"), `{"type":"function_call_output","call_id":"s","output":"ok"}`},
			[]string{"function_call:a", "function_call:b", "function_call_output:a", "local_shell_call:s", "function_call_output:b", "function_call_output:s"}},
		{"several notes keep their order",
			[]string{call("a"), call("b"), call("c"), res("a"), note("developer", "1"), res("b"), note("developer", "2"), res("c"), note("user", "3")},
			[]string{"function_call:a", "function_call:b", "function_call:c", "function_call_output:a", "function_call_output:b", "function_call_output:c", "message:", "message:", "message:"}},
	} {
		body := `{"model":"m","input":[` + strings.Join(c.in, ",") + `]}`
		if got := orderOf(t, joinToolRuns([]byte(body))); !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, c.want)
		}
	}
	// the notes' order is kept: 1, 2, 3
	body := `{"model":"m","input":[` + strings.Join([]string{call("a"), call("b"), call("c"), res("a"), note("developer", "n1"), res("b"), note("developer", "n2"), res("c"), note("user", "n3")}, ",") + `]}`
	got := string(joinToolRuns([]byte(body)))
	if i, j, k := strings.Index(got, "n1"), strings.Index(got, "n2"), strings.Index(got, "n3"); !(i < j && j < k) || strings.Index(got, `"call_id":"c","output"`) > i {
		t.Fatalf("notes out of order or before the last result: %s", got)
	}
	// a result written before its call (a client's odd replay) opens no
	// run, so nothing after it is held back to the end of the request
	for _, keep := range []string{
		`{"model":"m","input":[` + res("a") + `,` + call("a") + `,` + note("user", "next turn") + `,` + call("b") + `,` + res("b") + `]}`,
	} {
		if got := joinToolRuns([]byte(keep)); string(got) != keep {
			t.Fatalf("rewritten:\n%s\nto\n%s", keep, got)
		}
	}
}

// The same run translated for a Chat upstream: each call is answered by
// the result the client sent, not by the synthetic "unavailable" one
// pairToolMessages gives a call whose exchange an item closed.
func TestTranslatedToolRunKeepsItsRealResults(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	body := `{"model":"fake/m1","stream":true,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"look"}]},` +
		`{"type":"function_call","call_id":"call_a","name":"view_image","arguments":"{}"},` +
		`{"type":"function_call","call_id":"call_b","name":"exec_command","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_a","output":"looked"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"one is back"}]},` +
		`{"type":"function_call_output","call_id":"call_b","output":"ran"}]}`
	if code, out := post(t, "/v1/responses", body); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	var sent struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(f.got, &sent); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range sent.Messages {
		role, _ := m["role"].(string)
		if role == "tool" {
			role += ":" + fmt.Sprint(m["content"])
		}
		roles = append(roles, role)
	}
	if !slices.Contains(roles, "tool:looked") || !slices.Contains(roles, "tool:ran") || bytes.Contains(f.got, []byte("unavailable")) {
		t.Fatalf("the run's results went as %v\n%s", roles, f.got)
	}
}
