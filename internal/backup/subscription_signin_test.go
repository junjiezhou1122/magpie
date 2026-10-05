package backup

// What a backup must never carry: a subscription's sign-in. #880 asked for
// one and was closed as intentional — a subscription is the sign-in of an
// agent on one machine, so each machine signs in on its own, and a bundle
// that carried one would sign the agent in on the machine it was put on.
//
// The policy has two sides, and the second is the one the report tripped
// over. A subscription the user has moved onto its plugin still has a row
// in providers.json, and that row travels: it names the provider, so the
// receiving machine offers the subscription. What stays behind is the
// credential behind the row. The row travels, the credential does not.
//
// These tests pin that on the built-in side (logins.json) and the plugin
// side (plugin-auth.json) together, under both credential policies of
// Collect, and against a typed key as the control: a user-typed key does
// sync, so the policy is not "nothing credential-shaped syncs".

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The credentials in the fixtures below. Each appears only in a sign-in
// store, never in providers.json, so finding one in a bundle can only mean
// the sign-in store was read. The names are deliberately greppable: a
// reviewer can confirm at a glance that they are fixtures and not values
// copied out of a real machine.
const (
	// built-in sign-ins, logins.json
	fixtureCodexRefresh  = "fixture-codex-refresh-token"
	fixtureCodexAccess   = "fixture-codex-access-token"
	fixtureClaudeRefresh = "fixture-claude-refresh-token"
	// plugin sign-ins, plugin-auth.json
	fixtureGrokRefresh = "fixture-grok-refresh-token"
	fixtureGrokAccess  = "fixture-grok-access-token"
	fixtureGrokSlot    = "fixture-grok-work-refresh-token"
	fixtureGeminiKey   = "fixture-gemini-plugin-api-key"
	// a user-typed key: this one is meant to sync
	fixtureTypedKey  = "fixture-typed-key"
	fixtureSecondKey = "fixture-typed-second-key"
	fixtureBalance   = "fixture-balance-token"
	typedProviderID  = "acme-typed"
	// movedProviderID is a subscription the user moved onto its plugin, from
	// the OpenCode catalog. The move retires the built-in's own id
	// (tidyMoved) and what the user is left with is the plugin's provider:
	// a row in providers.json with no key on it, its sign-in in
	// plugin-auth.json, and the same account listed in logins.json under
	// "plugin:"+id, which follows the plugin's auth
	// (provider/plugin_accounts.go). That row is the one a backup has to
	// carry, because it is the only thing on the receiving machine that
	// offers the subscription at all.
	//
	// opencode-zen is a real NoKey preset, so a row with no key on it is a
	// row magpie stores rather than one this test had to force past Save.
	movedProviderID = "opencode-zen"
)

// signIns writes both sign-in stores the way magpie keeps them after the
// user has signed in twice on this machine: two built-in agents in
// logins.json, and the moved subscription in both stores, one plugin
// account under its provider's id and a second under id#slot. More than
// one of each, so a store that were read and then filtered down to its
// first entry would still be caught.
func signIns(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	writeJSON(t, filepath.Join(settings.Dir(), "logins.json"), []map[string]any{
		{"agent": "codex", "user": "me@example.com", "plan": "plus", "seen": seen, "on": true,
			"auth": map[string]any{"tokens": map[string]any{
				"refresh_token": fixtureCodexRefresh, "access_token": fixtureCodexAccess}}},
		{"agent": "claude", "user": "me@example.com", "plan": "max", "seen": seen,
			"auth": map[string]any{"refreshToken": fixtureClaudeRefresh}},
		// a moved subscription is listed here too, under the plugin's id,
		// with no auth of its own: the plugin's auth is the truth and
		// logins.json follows it.
		{"agent": "plugin:" + movedProviderID, "user": "me@example.com", "plan": "pro",
			"seen": seen, "on": true, "home": movedProviderID},
	})
	writeJSON(t, plugin.AuthPath(), map[string]map[string]any{
		movedProviderID:           {"type": "oauth", "refresh": fixtureGrokRefresh, "access": fixtureGrokAccess},
		movedProviderID + "#work": {"type": "oauth", "refresh": fixtureGrokSlot},
		"opencode-copilot":        {"type": "api", "key": fixtureGeminiKey},
	})
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// subscriptionMachine is a machine with a subscription signed in on it,
// moved onto its plugin, and one provider the user typed a key into. Both
// sign-in stores hold credentials, so the two stores and providers.json
// are three different places, and only one of them may reach a bundle.
func subscriptionMachine(t *testing.T) {
	t.Helper()
	for _, p := range []provider.Provider{
		{ID: movedProviderID, Name: "OpenCode Zen", Preset: "opencode-zen",
			Chat: "https://opencode.ai/zen/v1"},
		{ID: typedProviderID, Name: "Acme Typed", Chat: "https://acme.example.com/v1",
			Key: fixtureTypedKey, BalanceToken: fixtureBalance,
			Keys: []provider.KeyAccount{{Name: "second", Key: fixtureSecondKey}}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	signIns(t)
}

// carried is the bundle as the bytes of a sealed backup would hold them.
// Seal marshals the whole Bundle in one call (backup.go:252), so these are
// the bytes that get sealed: a credential that does not appear in them in
// plain text does not reach a sealed backup in plain text either, and
// reading the bundle's own bytes says that without guessing which of its
// fields a sign-in might hide in.
//
// That is a statement about plain text, and plain text is what the policy
// is about: what must not reach another machine is a credential that
// machine can read. It is not a proof that no sign-in can be in there. A
// []byte field is base64 in these bytes — Bundle.Icons is
// map[string][]byte — so an account that rode in under one of them would
// sit in a sealed backup with this check still green. Measured, not
// argued: a Collect that copies plugin-auth.json into
// b.Icons["auth-state.bin"] puts the whole plugin store in the bundle and
// leaves every test here passing.
//
// Two limits are written down rather than fixed. strings.Contains looks for
// a literal, and JSON escapes what a credential may legitimately hold (<
// and & become \u003c and \u0026, non-ASCII becomes \uXXXX), so an
// escaped credential would pass it; every fixture here is ASCII
// alphanumerics and hyphens, so none is. And if Seal ever stops being
// json.Marshal of the Bundle and marshals a wire type of its own, carried()
// stops describing the sealed bytes and has to move with it. Bundle already
// holds a type with a MarshalJSON of its own (profile.Profile, in
// Profiles), so this repo does add custom serializers — but such a type is
// marshalled by carried() and by Seal alike, so the two do not drift apart
// today.
func carried(t *testing.T, b Bundle) string {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// signInSecrets is every credential signIns wrote. None of them is in
// providers.json, so finding one in a bundle means a sign-in store was read.
func signInSecrets() []string {
	return []string{
		fixtureCodexRefresh, fixtureCodexAccess, fixtureClaudeRefresh,
		fixtureGrokRefresh, fixtureGrokAccess, fixtureGrokSlot, fixtureGeminiKey,
	}
}

// TestCollectLeavesSubscriptionSignInsOut is the policy itself: under both
// credential policies, no built-in and no plugin sign-in reaches a bundle.
//
// The four combinations matter. A guard written only on the keys=false path
// passes here but leaks a keyed backup; one written only over the built-in
// store passes but leaks every moved subscription, which is the case #880
// was filed about.
func TestCollectLeavesSubscriptionSignInsOut(t *testing.T) {
	for _, keys := range []bool{false, true} {
		name := "keyless"
		if keys {
			name = "keyed"
		}
		t.Run(name, func(t *testing.T) {
			home(t)
			appdir.UseExecutable("")
			subscriptionMachine(t)

			b, err := Collect(keys, "test")
			if err != nil {
				t.Fatal(err)
			}
			body := carried(t, b)
			for _, secret := range signInSecrets() {
				if strings.Contains(body, secret) {
					t.Errorf("a subscription sign-in reached a %s bundle: %q is in it", name, secret)
				}
			}
			// The credential stayed behind, not on this machine's store: the
			// bundle leaves the sign-ins where they were, in place.
			for _, path := range []string{filepath.Join(settings.Dir(), "logins.json"), plugin.AuthPath()} {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("collecting a %s bundle disturbed this machine's sign-ins: %v", name, err)
				}
			}
		})
	}
}

// TestCollectCarriesTheRowOfAMovedSubscription is the asymmetry, said out
// loud: a subscription the user moved onto its plugin still has a row in
// providers.json, and that row travels, so the machine the bundle lands on
// offers the subscription at all. What stays behind is the credential behind
// the row — TestCollectLeavesSubscriptionSignInsOut covers that half, and a
// row arriving with no sign-in is what leaves the receiving machine showing
// the provider as needing a sign-in.
func TestCollectCarriesTheRowOfAMovedSubscription(t *testing.T) {
	for _, keys := range []bool{false, true} {
		name := "keyless"
		if keys {
			name = "keyed"
		}
		t.Run(name, func(t *testing.T) {
			home(t)
			appdir.UseExecutable("")
			subscriptionMachine(t)

			b, err := Collect(keys, "test")
			if err != nil {
				t.Fatal(err)
			}
			row := providerRow(t, b, movedProviderID)
			if row.Chat != "https://opencode.ai/zen/v1" {
				t.Errorf("the moved subscription's row lost its details: %+v", row)
			}
			// The row travels, and it travels as it is: Collect added no
			// credential to it out of the sign-in store. What the row holds
			// is what the local store holds — opencode-zen is a NoKey preset,
			// so that is OpenCode's own well-known anonymous key
			// (provider.OpenCodeAnonymousKey), not anything the user signed
			// in with — and under a keyless policy the row's own key is
			// blanked like any other provider's. Either way the bundle holds
			// no sign-in behind the row, which is the whole of the
			// asymmetry: the row travels, the credential does not.
			// Headers is not compared here. A credential riding in under a
			// header name would not be caught by this assertion; it is
			// caught by TestCollectLeavesSubscriptionSignInsOut, which
			// looks for it anywhere in the bundle, and that is where the
			// coverage lives. This one says only that the row's own
			// key fields are the local ones.
			local, err := provider.Stored()
			if err != nil {
				t.Fatal(err)
			}
			before := storedRow(t, local, movedProviderID)
			want := before
			if !keys {
				want = withoutKeys(want)
			}
			if row.Key != want.Key || len(row.Keys) != len(want.Keys) || row.BalanceToken != want.BalanceToken {
				t.Errorf("the moved subscription's row came back with a credential Collect added: local=%+v, in the bundle key=%q keys=%v balance=%q",
					before, row.Key, row.Keys, row.BalanceToken)
			}
		})
	}
}

// TestCollectCarriesTypedKeysOnlyWhenAsked is the control that keeps the
// policy above from being read as "nothing credential-shaped syncs". A key
// the user typed is not an agent's sign-in on this machine: it is a note
// the user wrote down, and it is the one credential magpie's backups have
// always carried. It goes in when keys are asked for and stays out when
// they are not, on all three of the places a provider holds one.
func TestCollectCarriesTypedKeysOnlyWhenAsked(t *testing.T) {
	for _, keys := range []bool{false, true} {
		name := "keyless"
		if keys {
			name = "keyed"
		}
		t.Run(name, func(t *testing.T) {
			home(t)
			appdir.UseExecutable("")
			subscriptionMachine(t)

			b, err := Collect(keys, "test")
			if err != nil {
				t.Fatal(err)
			}
			row := providerRow(t, b, typedProviderID)
			if keys {
				if row.Key != fixtureTypedKey || row.BalanceToken != fixtureBalance {
					t.Errorf("a keyed bundle left a typed key or balance token out: %+v", row)
				}
				if len(row.Keys) != 1 || row.Keys[0].Key != fixtureSecondKey {
					t.Errorf("a keyed bundle left a typed second key out: %+v", row.Keys)
				}
				return
			}
			if row.Key != "" || row.BalanceToken != "" || len(row.Keys) != 0 {
				t.Errorf("a keyless bundle carried a typed credential: %+v", row)
			}
			// The key is out of the bundle, not merely blanked in it.
			if strings.Contains(carried(t, b), fixtureTypedKey) {
				t.Error("a keyless bundle still holds the typed key")
			}
		})
	}
}

// TestRestoreLeavesLocalSignInsAlone is the other end of the policy: a
// bundle that cannot carry a sign-in also cannot take one away, so
// restoring one onto a machine that is already signed in leaves the machine
// signed in on what it signed in with.
//
// What is compared is the sign-ins — the accounts, and the credential
// behind each — and not the bytes they happen to be written in. The policy
// is that each machine signs in on its own; the identity of the file the
// sign-ins sit in is a stronger claim than that, and an incidental
// reformat of logins.json would fail a byte comparison with nothing about
// the policy changed. That is not hypothetical: re-indenting logins.json
// between the read and the restore, same accounts and same credentials,
// turned a byte comparison here red.
//
// The honest limit of that choice: nothing in this package names either
// file outside this test, so Restore never opens them, and the direction
// of the policy is all this can characterise. It is deliberately not
// written as a claim that those files are untouchable — a future restore
// that did own them would need a real assertion of its own, not this one.
func TestRestoreLeavesLocalSignInsAlone(t *testing.T) {
	home(t)
	appdir.UseExecutable("")
	subscriptionMachine(t)
	beforeBuiltIn := builtInSignIns(t)
	beforePlugin := pluginSignIns(t)

	if _, err := Restore(Bundle{Version: BundleVersion, Keys: true}, All); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(Bundle{Version: BundleVersion}, All); err != nil {
		t.Fatal(err)
	}

	if after := builtInSignIns(t); !sameSignIns(beforeBuiltIn, after) {
		t.Errorf("restoring a backup changed this machine's built-in sign-ins: %s is now %s",
			signInsString(beforeBuiltIn), signInsString(after))
	}
	if after := pluginSignIns(t); after != beforePlugin {
		t.Errorf("restoring a backup changed this machine's plugin sign-ins: %s is now %s", beforePlugin, after)
	}
	// And the sign-ins are there to be read, not merely equal to whatever
	// was read before. Without this the two comparisons above would also
	// pass on a pair of empty reads.
	have := signInsString(builtInSignIns(t)) + pluginSignIns(t)
	for _, secret := range signInSecrets() {
		if !strings.Contains(have, secret) {
			t.Errorf("the local sign-in %q is gone after a restore", secret)
		}
	}
}

// localSignIn is one row of logins.json as far as this policy is concerned:
// who is signed in, and the credential that signs them in. The rest of a
// row — the plan, when it was last seen, where it sits in the list — is not
// part of "this machine signs in on its own", so it is not read here.
type localSignIn struct {
	Agent string
	User  string
	Home  string
	Auth  string
}

// builtInSignIns reads logins.json as the sign-ins it holds, with each
// row's credential canonicalised and the rows put in a fixed order, so that
// how the file was written is not part of what is compared.
func builtInSignIns(t *testing.T) []localSignIn {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(settings.Dir(), "logins.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Agent string          `json:"agent"`
		User  string          `json:"user"`
		Home  string          `json:"home"`
		Auth  json.RawMessage `json:"auth"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("this machine's logins.json does not read: %v", err)
	}
	out := make([]localSignIn, 0, len(rows))
	for _, r := range rows {
		out = append(out, localSignIn{r.Agent, r.User, r.Home, canonical(t, r.Auth)})
	}
	slices.SortFunc(out, func(a, b localSignIn) int {
		return cmp.Or(cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.User, b.User), cmp.Compare(a.Home, b.Home))
	})
	return out
}

// pluginSignIns reads plugin-auth.json the same way: the accounts it holds
// and what each of them keeps, canonicalised.
func pluginSignIns(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(plugin.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	return canonical(t, raw)
}

// canonical re-marshals JSON so two documents compare by what they say.
// Go writes a map's keys in order, so the result follows the content alone
// and not the indentation, the key order on the page, or the trailing
// newline. A field that is not there at all reads as the null it means, so
// an absent credential and an explicit null compare equal.
func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	if len(bytes.TrimSpace(raw)) == 0 {
		return "null"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s does not read as JSON: %v", raw, err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sameSignIns reports whether the two reads name the same accounts with the
// same credentials, each read already in a fixed order.
func sameSignIns(a, b []localSignIn) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// signInsString is a machine's sign-ins as one line a failure can print.
func signInsString(ls []localSignIn) string {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, fmt.Sprintf("%s %s home=%s auth=%s", l.Agent, l.User, l.Home, l.Auth))
	}
	return strings.Join(parts, "; ")
}

func providerRow(t *testing.T, b Bundle, id string) provider.Provider {
	t.Helper()
	for _, p := range b.Providers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("the bundle has no row for %q; it has %v", id, providerIDs(b.Providers))
	return provider.Provider{}
}

// storedRow finds one provider among a machine's own rows, the way
// providerRow finds one in a bundle's, so a test can hold the two side by
// side and say what did and did not change on the way.
func storedRow(t *testing.T, rows []provider.Provider, id string) provider.Provider {
	t.Helper()
	for _, p := range rows {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("the store has no row for %q; it has %v", id, providerIDs(rows))
	return provider.Provider{}
}

func providerIDs(rows []provider.Provider) []string {
	var ids []string
	for _, p := range rows {
		ids = append(ids, p.ID)
	}
	return ids
}

// TestScopedKeysFallsBackToTheWholeBundleBit covers the part of
// scopedKeys the report noticed, and it is written to say only what is
// true whichever way that gap is closed.
//
// scopedKeys has a case for "settings" and one for "library"; "providers"
// falls through to the whole-bundle Keys bit. The maintainer of #880
// acknowledged the gap and said giving providers a switch of its own needs
// `case "providers": own = b.ProvidersKeys` first.
//
// This test deliberately does not pin that fall-through, and the reason is
// not caution but that pinning it would be wrong in both directions. There
// is nothing to observe: scopedKeys is called once in this package, with
// "settings" (backup.go:401), so "providers" and "library" have no caller
// and an assertion about either would pin an unexercised implementation
// detail rather than a behaviour. And a test that did pin it would fail
// for a good reason the moment the case was added — a maintainer reading
// "the backup rejects a per-part provider key scope" off a red test would
// revert a correct fix. That is a worse outcome than the gap going
// unnoticed for a release.
//
// What is asserted instead holds both before and after that fix, and is
// what the report's own wording pins: a part with no marker of its own is
// decided by the whole-bundle bit. Adding `case "providers"` does not
// change that, because the case only reads the marker, and a bundle
// without one still has none. So this test stays green across the fix, and
// says on the way what still needs deciding: whether a providers marker,
// once there is a caller to read it, is honoured or overridden.
func TestScopedKeysFallsBackToTheWholeBundleBit(t *testing.T) {
	for _, part := range []string{"settings", "library", "providers"} {
		t.Run(part, func(t *testing.T) {
			if !(Bundle{Keys: true}).scopedKeys(part) {
				t.Errorf("%q lost a whole bundle's keys", part)
			}
			if (Bundle{}).scopedKeys(part) {
				t.Errorf("%q gained a keyless bundle's keys", part)
			}
		})
	}
}

// TestScopedKeysPrefersAPartsOwnMarker is the switch working: where a part
// has a marker, that marker decides, either way round against the
// whole-bundle bit. It is what makes ProvidersKeys meaningful the day
// something reads it.
func TestScopedKeysPrefersAPartsOwnMarker(t *testing.T) {
	for _, part := range []string{"settings", "library"} {
		t.Run(part, func(t *testing.T) {
			over := Bundle{Keys: true}
			setScopedMarker(&over, part, false)
			if over.scopedKeys(part) {
				t.Errorf("%q ignored its own marker and took the whole bundle's keys", part)
			}
			under := Bundle{}
			setScopedMarker(&under, part, true)
			if !under.scopedKeys(part) {
				t.Errorf("%q ignored its own marker and took the keyless bundle's", part)
			}
		})
	}
}

// setScopedMarker writes the marker a sync writes for one part, by the
// name it is serialized under, so the test names parts the way a bundle
// does rather than reaching past the JSON into the struct.
func setScopedMarker(b *Bundle, part string, v bool) {
	raw, err := json.Marshal(map[string]bool{part + "Keys": v})
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(raw, b); err != nil {
		panic(err)
	}
}
