package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// user-agent=agent has a key+URL provider pass the agent's User-Agent on
// (#1851), user-agent=magpie goes back to magpie's, and a signed-in
// account, which goes as its own client, is refused it.
func TestProviderUserAgentPair(t *testing.T) {
	p := provider.Provider{ID: "plan", Name: "Plan", Chat: "https://plan.example/v1", Key: "k"}
	if err := applyPairs(&p, []string{"user-agent=agent"}); err != nil || !p.PassUserAgent {
		t.Fatalf("agent: %v %v", p.PassUserAgent, err)
	}
	if err := applyPairs(&p, []string{"user-agent=magpie"}); err != nil || p.PassUserAgent {
		t.Fatalf("magpie: %v %v", p.PassUserAgent, err)
	}
	if err := applyPairs(&p, []string{"user-agent=claude"}); err == nil || !strings.Contains(err.Error(), "agent|magpie") {
		t.Fatalf("a made-up value: %v", err)
	}
	a := provider.Provider{ID: "zcode", Name: "ZCode", Account: &provider.Account{Agent: "zcode"}}
	if err := applyPairs(&a, []string{"user-agent=agent"}); err == nil || a.PassUserAgent {
		t.Fatalf("an account took it: %v", err)
	}
	if err := applyPairs(&a, []string{"user-agent=magpie"}); err != nil {
		t.Fatalf("an account turning it off: %v", err)
	}
}
