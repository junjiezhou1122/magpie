package tune

import (
	"fmt"
	"testing"
	"time"
)

// session makes one conversation: it starts at start tokens, grows by grow
// a call, and compacts down to after when it reaches at, gap apart.
func session(id string, t0 time.Time, n, start, grow, at, after int, gap time.Duration, wrote1 bool) []Call {
	var out []Call
	p := start
	for i := 0; i < n; i++ {
		c := Call{Time: t0.Add(time.Duration(i) * gap), Stream: id, Model: "claude-opus-5-5", Prompt: p, Out: 800, Wrote: grow}
		if wrote1 {
			c.Wrote1 = c.Wrote
		}
		out = append(out, c)
		p += grow
		if at > 0 && p >= at {
			p = after
		}
	}
	return out
}

func many(k int, f func(id string, t0 time.Time) []Call) []Call {
	var out []Call
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < k; i++ {
		out = append(out, f(fmt.Sprint("s", i), t0.Add(time.Duration(i)*24*time.Hour))...)
	}
	return out
}

var claudeOpts = CompactOpts{Min: 100000, Max: 1000000, Step: 10000, Reserve: 33000}

// Long conversations on the whole 1M window carry a huge prompt for every
// call: a smaller window reads far less.
func TestCompactLongSessionsGoSmaller(t *testing.T) {
	cs := many(3, func(id string, t0 time.Time) []Call {
		return session(id, t0, 600, 30000, 5000, 967000, 60000, 30*time.Second, false)
	})
	o := claudeOpts
	o.Window, o.Current = 1000000, 1000000
	r := Compact(cs, o)
	if r == nil {
		t.Fatal("no advice")
	}
	if r.Best >= 1000000 || (r.Cost-r.Low)/r.Cost < 0.2 {
		t.Fatalf("best %d saves %.0f of %.0f: want a smaller window saving a fifth or more", r.Best, r.Cost-r.Low, r.Cost)
	}
	if r.Facts.Compacts == 0 || r.Facts.Growth != 5000 {
		t.Fatalf("facts %+v", r.Facts)
	}
}

// Conversations that never come near the window: no window changes what
// they cost, so it stays as the user has it.
func TestCompactShortSessionsKeepCurrent(t *testing.T) {
	cs := many(5, func(id string, t0 time.Time) []Call {
		return session(id, t0, 30, 20000, 2000, 0, 0, time.Minute, false)
	})
	o := claudeOpts
	o.Window = 200000
	r := Compact(cs, o)
	if r == nil {
		t.Fatal("no advice")
	}
	if r.Best != r.Current || r.Current != 200000 || r.Low != r.Cost {
		t.Fatalf("got %d → %d (%.0f → %.0f), want 200000 kept", r.Current, r.Best, r.Cost, r.Low)
	}
}

// Too few calls to go on: no advice at all.
func TestCompactNeedsCalls(t *testing.T) {
	cs := session("s", time.Now(), 20, 20000, 2000, 0, 0, time.Minute, false)
	if r := Compact(cs, claudeOpts); r != nil {
		t.Fatalf("advice from 20 calls: %+v", r)
	}
}

// A user who comes back after 20 minutes each time: a 5-minute cache is
// written again on every call, an hour's is read.
func TestCacheTTLPausesWantAnHour(t *testing.T) {
	cs := many(4, func(id string, t0 time.Time) []Call {
		return session(id, t0, 40, 30000, 3000, 0, 0, 20*time.Minute, false)
	})
	r := CacheTTL(cs, "")
	if r == nil || r.Current != "5m" || r.Best != "1h" || r.Low >= r.Cost {
		t.Fatalf("got %+v, want 5m → 1h", r)
	}
	if r.Facts.Pause < 0.99 {
		t.Fatalf("pause share %.2f", r.Facts.Pause)
	}
}

// A user whose calls all come within seconds pays the hour's dearer writes
// for nothing.
func TestCacheTTLQuickWantsFiveMinutes(t *testing.T) {
	cs := many(4, func(id string, t0 time.Time) []Call {
		return session(id, t0, 40, 30000, 3000, 0, 0, 20*time.Second, true)
	})
	r := CacheTTL(cs, "")
	if r == nil || r.Current != "1h" || r.Best != "5m" {
		t.Fatalf("got %+v, want 1h (read from the writes) → 5m", r)
	}
}

// A setting over the model's window runs at the window: Claude Code takes
// the smaller of the two, so that is what the advice starts from.
func TestCompactSettingCappedByWindow(t *testing.T) {
	cs := many(3, func(id string, t0 time.Time) []Call {
		return session(id, t0, 400, 30000, 1000, 167000, 57000, 30*time.Second, false)
	})
	o := claudeOpts
	o.Window, o.Current = 200000, 500000
	r := Compact(cs, o)
	if r == nil || r.Current != 200000 || r.Window != 200000 {
		t.Fatalf("got %+v, want current 200000", r)
	}
	if last := r.Curve[len(r.Curve)-1].Value; last > 200000 {
		t.Fatalf("curve reaches %d, past the model's window", last)
	}
}

// Each window tried says how many compactions it would have made (#1860):
// fewer as the window grows, none once it is over every conversation, and
// at the window the agent ran with, about as many as its files show.
func TestCompactCurveCountsCompactions(t *testing.T) {
	cs := many(3, func(id string, t0 time.Time) []Call {
		return session(id, t0, 600, 30000, 5000, 967000, 60000, 30*time.Second, false)
	})
	o := claudeOpts
	o.Window, o.Current = 1000000, 1000000
	r := Compact(cs, o)
	if r == nil {
		t.Fatal("no advice")
	}
	first, last := r.Curve[0], r.Curve[len(r.Curve)-1]
	if first.Compacts <= last.Compacts || last.Compacts == 0 {
		t.Fatalf("%d compactions at %d, %d at %d: want more at the smaller window, some at the largest", first.Compacts, first.Value, last.Compacts, last.Value)
	}
	for i := 1; i < len(r.Curve); i++ {
		if r.Curve[i].Compacts > r.Curve[i-1].Compacts {
			t.Fatalf("%d compactions at %d, more than %d at %d", r.Curve[i].Compacts, r.Curve[i].Value, r.Curve[i-1].Compacts, r.Curve[i-1].Value)
		}
	}
	if d := last.Compacts - r.Facts.Compacts; d < -3 || d > 3 {
		t.Fatalf("%d compactions replayed at %d, %d seen in the files", last.Compacts, last.Value, r.Facts.Compacts)
	}
}
