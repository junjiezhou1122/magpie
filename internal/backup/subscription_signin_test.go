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
	"encoding/json"
	"os"
	"path/filepath"
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

// carried is every string in the bundle as it would be written to a file.
// Seal marshals the bundle with exactly this call, so a credential absent
// from this string cannot be inside a sealed backup either — which is what
// lets these tests read the bundle's own bytes instead of guessing which of
// its fields a sign-in might hide in.
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
// signed in. The sign-ins are checked byte for byte, since a restore that
// rewrote either file with the same accounts would still be a machine whose
// sign-ins the backup touched.
func TestRestoreLeavesLocalSignInsAlone(t *testing.T) {
	home(t)
	appdir.UseExecutable("")
	subscriptionMachine(t)
	logins := filepath.Join(settings.Dir(), "logins.json")
	before := map[string][]byte{}
	for _, path := range []string{logins, plugin.AuthPath()} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = b
	}

	if _, err := Restore(Bundle{Version: BundleVersion, Keys: true}, All); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(Bundle{Version: BundleVersion}, All); err != nil {
		t.Fatal(err)
	}

	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("restoring a backup disturbed this machine's sign-ins at %s: %v", filepath.Base(path), err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("restoring a backup rewrote %s: %q is now %q", filepath.Base(path), want, got)
		}
	}
	// And the accounts are still there to be read, not just their bytes.
	if logins, err := os.ReadFile(logins); err != nil || !bytes.Contains(logins, []byte(fixtureCodexRefresh)) {
		t.Errorf("the local built-in sign-in is gone after a restore: %v", err)
	}
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
