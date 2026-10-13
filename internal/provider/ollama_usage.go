package provider

// Ollama Cloud tells a key its account's money at ollama.com/api/balance
// (#1888): what is left of the plan's included allowance this period, and
// the credits bought on top of it. The allowance is a plan card with one
// window (PlanQuotas), the bought credits a balance card (KeyBalances).

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ollamaBalanceURL is a var for tests to point at a server of their own.
var ollamaBalanceURL = "https://ollama.com/api/balance"

// ollamaBalance is the reply:
//
//	{"included":{"balance_usd":72.5,"allowance_usd":100,
//	   "period":{"from":"2026-09-15T09:30:00Z","until":"2026-10-15T09:30:00Z"}},
//	 "purchased":{"balance_usd":25.0}}
//
// Each amount is kept as it came, so one left out (a plan with no included
// allowance, an account that never bought credits) is unknown, not $0.
type ollamaBalance struct {
	Included *struct {
		Balance   any `json:"balance_usd"`
		Allowance any `json:"allowance_usd"`
		Period    *struct {
			From  string `json:"from"`
			Until string `json:"until"`
		} `json:"period"`
	} `json:"included"`
	Purchased *struct {
		Balance any `json:"balance_usd"`
	} `json:"purchased"`
}

// errNoBalance is a reply that tells no balance for this card to show: not
// a failure to say on one, so the card is left out.
var errNoBalance = errors.New("no balance in the reply")

func parseOllamaBalance(b []byte) (ollamaBalance, error) {
	var r ollamaBalance
	if err := json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	return r, nil
}

// readOllamaPlan is the included allowance as a window: what the period
// has spent of it, in USD, renewing at the period's end. A reply without
// an allowance or without what is left of it has no window, and no card.
// Spending past the allowance draws on the bought credits, so a spent
// allowance with credits left doesn't stop the key (Aside).
func readOllamaPlan(b []byte) (string, []QuotaWindow, error) {
	r, err := parseOllamaBalance(b)
	if err != nil {
		return "", nil, err
	}
	out := []QuotaWindow{}
	in := r.Included
	if in == nil {
		return "", out, nil
	}
	allowance, ok1 := number(in.Allowance)
	left, ok2 := number(in.Balance)
	if !ok1 || !ok2 || allowance <= 0 {
		return "", out, nil
	}
	used := min(allowance, max(0, allowance-left))
	w := QuotaWindow{Name: "Allowance", Amount: used, Limit: allowance, Unit: "USD",
		Used: min(100, max(0, used/allowance*100))}
	if p := in.Period; p != nil {
		until, uerr := time.Parse(time.RFC3339, p.Until)
		if uerr == nil {
			w.ResetsAt = &until
		}
		if from, ferr := time.Parse(time.RFC3339, p.From); ferr == nil && uerr == nil && until.After(from) {
			w.Span = until.Sub(from)
			if d := w.Span.Hours() / 24; d >= 27 && d <= 32 {
				w.Name = "Month"
			}
		}
	}
	if r.Purchased != nil {
		if bought, ok := number(r.Purchased.Balance); ok && bought > 0 {
			w.Aside = true
		}
	}
	return "", append(out, w), nil
}

// readOllamaCredits is the credits bought on top of the plan, in dollars.
// A reply that names none has no balance card: the allowance is the plan
// card's.
func readOllamaCredits(b []byte) (string, error) {
	r, err := parseOllamaBalance(b)
	if err != nil {
		return "", err
	}
	if r.Purchased == nil {
		if r.Included == nil {
			return "", fmt.Errorf("Ollama's reply has no included or purchased balance: %s", shapeOf(b))
		}
		return "", errNoBalance
	}
	v, ok := number(r.Purchased.Balance)
	if !ok {
		return "", errNoBalance
	}
	return money("$", v), nil
}
