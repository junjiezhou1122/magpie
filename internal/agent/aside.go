package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	syncatomic "sync/atomic"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

// Aside is the browser's own coding agent. It reads the same provider list
// Pi does — magpie hands it the whole catalog in a provider block of its own
// — but its default is one object naming the provider, the model, the
// thinking level and fast mode together, so its model field reads and writes
// two keys of settings.json rather than Pi's pair of strings.
func aside(home string) *Agent { return asideIn(here(home)) }

func asideIn(at place) *Agent {
	dir := asideDir(at)
	path := filepath.Join(dir, "settings.json")
	modelsPath := filepath.Join(dir, "models.json")
	// the provider block magpie owns: Pi's shape, which Aside reads the same
	// way, carrying the token that names Aside as the caller so its requests
	// are told apart from anyone else's
	block := func() any {
		p := magpieProviderJSONAt("pi", "aside", at.gw()).(map[string]any)
		p["apiKey"] = gateway.TokenFor("aside")
		return p
	}
	writeMagpie := func() error {
		// A provider of magpie's name carrying a token of someone else's is
		// the user's, or another tool's, and is not taken over. One of ours
		// is repaired instead: magpie's own left at another address is drift
		// (another magpie's port, a config someone else edited), and the
		// same is what Reapply is for.
		if key, ok := edit.GetJSON(modelsPath, "providers."+magpieID+".apiKey"); ok && key != gateway.TokenFor("aside") {
			return fmt.Errorf("Aside has a %q provider of its own, with the key %s — rename it to wire magpie in", magpieID, key)
		}
		return edit.SetJSON(modelsPath, edit.KV{Path: "providers." + magpieID, Value: block()})
	}
	// A model of magpie's needs the provider block to reach it through, and
	// what was on that key is remembered, so taking magpie out puts every key
	// back rather than only the default.
	wire := func(key, was string) error {
		if err := writeMagpie(); err != nil {
			return err
		}
		if was != "" {
			stash(map[string]string{at.key(key): was})
		}
		return nil
	}
	a := &Agent{
		ID: "aside", Name: "Aside", Icon: "aside", Bin: "aside", Dir: dir, Path: path,
		Notice: asideNoticeRestart,
		Check: func() string {
			onMagpie := strings.HasPrefix(asideModel(path, "defaultModel"), magpieID+"/")
			for _, role := range asideRoles {
				// a role of magpie's is on the same provider, so it is on the
				// same block: the default alone does not cover it
				if strings.HasPrefix(asideModel(path, "modelCategories."+role), magpieID+"/") {
					onMagpie = true
				}
			}
			if !onMagpie {
				return "" // on a model of its own: nothing of magpie's is in the way
			}
			return wiringOff("Aside", modelsPath,
				func(k string) (string, bool) { return edit.GetJSON(modelsPath, "providers."+magpieID+"."+k) },
				"baseUrl", at.v1(), "apiKey", gateway.TokenFor("aside"))
		},
		Sync: func() error {
			return syncJSON(modelsPath, "providers."+magpieID, block)
		},
		Fields: []Field{
			{
				Key: "model", Label: "model",
				// what a new session starts on, as Aside's default has it
				Get: func() string { return asideModel(path, "defaultModel") },
				Set: func(v string) error {
					if v == "" {
						return asideDefault(at, path, modelsPath)
					}
					p, m, ok := strings.Cut(v, "/")
					if !ok || p == "" || m == "" {
						return fmt.Errorf("expected provider/model, got %q", v)
					}
					if p == magpieID {
						// the model it had before is what the default puts back
						was := ""
						if cur, _ := edit.GetJSON(path, "defaultModel.provider"); cur != magpieID {
							was = asideModel(path, "defaultModel")
						}
						if err := wire("aside.was", was); err != nil {
							return err
						}
					}
					// the thinking level and fast mode beside the model are the
					// user's, and are left as they are
					return asideApplyDefault(path, map[string]string{
						"provider": p, "modelId": m,
					})
				},
				Options: func(cur map[string]string) []Option {
					return append(ownOptions(filepath.Join(dir, "models.json"), cur["model"]), viaMagpie("aside", magpieID+"/")...)
				},
			},
			{
				// Aside's own thinking level, under the same default the model
				// is in: for a model of magpie's, the levels are those of the
				// entry magpie wrote for it, and a level it does not offer is
				// shown as the one it runs at (as for Pi, #597).
				Key: "effort", Label: "thinking",
				Get: func() string {
					v, _ := edit.GetJSON(path, "defaultModel.thinkingLevel")
					if offered := piOffered("aside", asideModel(path, "defaultModel")); v != "" && offered != nil {
						return piClamp(v, offered)
					}
					return v
				},
				Set: func(v string) error {
					if cur, _ := edit.GetJSON(path, "defaultModel.thinkingLevel"); v == cur {
						return nil
					}
					if v == "" {
						return edit.DelJSON(path, "defaultModel.thinkingLevel")
					}
					return asideApplyDefault(path, map[string]string{"thinkingLevel": v})
				},
				Options: func(cur map[string]string) []Option {
					if offered := piOffered("aside", cur["model"]); offered != nil {
						return static(offered...)
					}
					return static(piLevels...)
				},
			},
		},
	}
	a.Fields = append(a.Fields, asideRoleFields(path, modelsPath, wire)...)
	return a
}

// asideRoles are the tasks Aside picks a model by hand, from the ones it
// offers in its own settings. Each is its own model in Aside's account, unset
// while it follows the default model.
var asideRoles = []string{"fast", "standard", "deep", "visual"}

// asideRoleFields are the role models, as magpie fields: Quiet, so a role the
// user has not set is not listed as a line of its own, and taking the model
// after the default while it is unset.
func asideRoleFields(path, modelsPath string, wire func(key, was string) error) []Field {
	out := make([]Field, 0, len(asideRoles))
	for _, role := range asideRoles {
		out = append(out, Field{
			Key: role, Label: role, Quiet: true, Follows: "model",
			// a role with no model of its own reads as nothing, which is how it
			// follows the default
			Get: func() string { return asideModel(path, "modelCategories."+role) },
			Set: func(v string) error {
				if v != "" {
					p, _, ok := strings.Cut(v, "/")
					if !ok {
						return fmt.Errorf("expected provider/model, got %q", v)
					}
					if p == magpieID {
						// a role on one of magpie's models is no use without the
						// provider to reach it through, and what the role named
						// before is what the default puts back
						was := ""
						if cur, _ := edit.GetJSON(path, "modelCategories."+role+".provider"); cur != magpieID {
							was = asideModel(path, "modelCategories."+role)
						}
						if err := wire("aside.was."+role, was); err != nil {
							return err
						}
					}
				}
				return asideApplyRole(path, role, v)
			},
			Options: func(map[string]string) []Option {
				return append(ownOptions(modelsPath, ""), viaMagpie("aside", magpieID+"/")...)
			},
		})
	}
	return out
}

// asideAccount is the account magpie wires: its number, the folder under
// ~/.aside/u it keeps its files in, and the "u0" the CLI names it by — which
// is not the agent's own id, and is refused where an account is asked for.
const asideAccount = 0

// asideAccountID is that account as the CLI takes it on the command line.
func asideAccountID() string { return fmt.Sprintf("u%d", asideAccount) }

// asideDir is Aside's own folder for the account magpie wires, where it keeps
// the two files magpie edits. A machine with a second account (1, …) is not
// wired: magpie does not know which one the user is working in.
func asideDir(at place) string {
	return filepath.Join(at.home, ".aside", "u", strconv.Itoa(asideAccount))
}

// asideModel is the model one of Aside's selections names as provider/model,
// empty when it names none. The default and a task role are the same shape,
// kept apart by where they are in the account.
func asideModel(path, key string) string {
	p, _ := edit.GetJSON(path, key+".provider")
	m, _ := edit.GetJSON(path, key+".modelId")
	if p == "" || m == "" {
		return ""
	}
	return p + "/" + m
}

// Aside keeps its default in the running daemon and writes the file itself.
// Editing the file behind it leaves the daemon on what it already had —
// read back the same way, the setting still said the old model after magpie
// had written the new one — so the change goes through Aside's own settings
// API, which applies it at once and leaves the same file behind. The API is
// reached through the repl: the settings command has no set of its own.
var asideSet = func(account, expr string) error {
	out, err := proc.Command("aside", "repl", "--account", account, expr).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("aside: %s", bytes.TrimSpace(ee.Stderr))
		}
		return err
	}
	if !bytes.Contains(out, []byte(asideOK)) {
		return fmt.Errorf("aside did not take the change: %s", bytes.TrimSpace(out))
	}
	return nil
}

// asideOK is what the repl prints once the setting is in, so a run that
// answered something else is not taken for one that wrote.
const asideOK = "MAGPIE_ASIDE_OK"

// asideStale is set when a default was written to the file because Aside
// could not be asked, and so only takes hold when it next starts.
var asideStale syncatomic.Bool

// asideApplyDefault changes the keys of Aside's default named in kv.
func asideApplyDefault(path string, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	patch, err := json.Marshal(kv)
	if err != nil {
		return err
	}
	return asideApply(path, "const m = aside.settings.get('defaultModel');"+
		"const after = aside.settings.set('defaultModel', Object.assign({}, m, "+string(patch)+"));"+asideCheck("after.defaultModel", kv),
		kvPaths("defaultModel", kv), nil)
}

// asideApplyRole points one of Aside's task roles at a model, or takes the
// role off altogether (v empty), which puts it back to following the default.
func asideApplyRole(path, role, v string) error {
	if role == "" {
		return nil
	}
	// Aside holds the roles as one object and takes the whole of a role: its
	// thinking level is part of the value, not a setting beside it, so a role
	// keeps the level it was on and one that is new takes the default's.
	expr := "const c = aside.settings.get('modelCategories') || {}; const r = c[" + jsString(role) + "] || {};"
	if v == "" {
		expr += "delete c[" + jsString(role) + "];"
		expr += "const after = aside.settings.set('modelCategories', c);" +
			"if (after.modelCategories && after.modelCategories[" + jsString(role) + "]) throw new Error('Aside kept the " + role + " role');"
		return asideApply(path, expr, nil, []string{"modelCategories." + role})
	}
	p, m, ok := strings.Cut(v, "/")
	if !ok || p == "" || m == "" {
		return fmt.Errorf("expected provider/model, got %q", v)
	}
	sel := asideRoleSelection(path, role, p, m)
	b, err := json.Marshal(sel)
	if err != nil {
		return err
	}
	expr += "c[" + jsString(role) + "] = Object.assign({}, r, " + string(b) + "});"
	kv := map[string]string{"provider": p, "modelId": m}
	expr += "const after = aside.settings.set('modelCategories', c);" +
		asideCheck("((after.modelCategories || {})["+jsString(role)+"] || {})", kv)
	return asideApply(path, expr, []edit.KV{{Path: "modelCategories." + role, Value: sel}}, nil)
}

// asideCheck is the part of an expression that fails the change when the
// setting that came back is not the one asked for. Aside answers a set with
// the settings as they now are and drops what it will not take without a word
// (a role naming a model it has not resolved), so without this a change that
// went nowhere reads as one that landed.
func asideCheck(what string, kv map[string]string) string {
	out := ""
	for k, v := range kv {
		out += " || " + what + "." + k + " !== " + jsString(v)
	}
	return "if (" + strings.TrimPrefix(out, " || ") + ") throw new Error('Aside did not take it');"
}

// asideRoleSelection is the whole of a task role as Aside takes it: Aside
// keeps the level and fast mode inside the role, and refuses a role that has
// no level, so one is always named. A role that has its own keeps it, a new
// one takes the default's, and where neither is set it takes the level Aside
// gives its own roles.
func asideRoleSelection(path, role, p, m string) map[string]any {
	level, _ := edit.GetJSON(path, "modelCategories."+role+".thinkingLevel")
	if level == "" {
		level, _ = edit.GetJSON(path, "defaultModel.thinkingLevel")
	}
	if level == "" {
		level = "medium"
	}
	fast, _ := edit.GetJSON(path, "modelCategories."+role+".fastMode")
	return map[string]any{
		"provider": p, "modelId": m, "thinkingLevel": level, "fastMode": fast == "true",
	}
}

// asideApply runs one settings change through Aside, and where Aside cannot be
// reached writes to the file itself what it would have written: the value
// Aside reads at its next start, and the user is told to start it. set is the
// change and del is taking it back, one of which is nil.
func asideApply(path, expr string, set []edit.KV, del []string) error {
	expr += "console.log('" + asideOK + "')"
	if err := asideSet(asideAccountID(), expr); err == nil {
		asideStale.Store(false)
		return nil
	} else {
		var werr error
		if del != nil {
			werr = edit.DelJSON(path, del...)
		} else {
			werr = edit.SetJSON(path, set...)
		}
		if werr != nil {
			return errors.Join(err, werr)
		}
		asideStale.Store(true)
	}
	return nil
}

// kvPaths are the file paths a patch of a setting's keys writes, for where
// Aside could not be asked.
func kvPaths(prefix string, kv map[string]string) []edit.KV {
	out := make([]edit.KV, 0, len(kv))
	for k, v := range kv {
		out = append(out, edit.KV{Path: prefix + "." + k, Value: v})
	}
	return out
}

// jsString is a Go string as the JavaScript string literal it is, so a model
// id is never read as part of the expression around it.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// asideClearDefault takes the keys out of Aside's default, or puts them back
// to was, the same way asideApplyDefault does.
func asideClearDefault(path, was string) error {
	kv := map[string]string{}
	if p, m, ok := strings.Cut(was, "/"); ok && p != "" && m != "" {
		kv["provider"], kv["modelId"] = p, m
	}
	if len(kv) == 0 {
		// no default of magpie's to put back, and a default the user set
		// themselves is not ours to take apart: only the model comes off
		return edit.DelJSON(path, "defaultModel.provider", "defaultModel.modelId")
	}
	return asideApplyDefault(path, kv)
}

// asideNoticeRestart is what the Agents page says when a change could only be
// written to the file: Aside reads its settings when it starts, so the new
// model is not in use until it does. A change it took needs no restart, and
// one it dropped without a word (a role naming a model it cannot resolve yet)
// lands here, which is the same advice.
func asideNoticeRestart() string {
	if !asideStale.Load() {
		return ""
	}
	return "Aside reads its models when it starts: quit and reopen it for this to take hold"
}

// asideDefault takes magpie out of Aside: the provider block it wrote goes,
// and the provider and model the user had before it come back, for the default
// and for every task role magpie moved. Their thinking level and fast mode are
// already where they left them — magpie never writes those two keys — so only
// the model is put back, and a key the user set themselves while magpie was
// not in it is left alone entirely.
func asideDefault(at place, path, modelsPath string) error {
	if err := edit.DelJSON(modelsPath, "providers."+magpieID); err != nil {
		return err
	}
	if cur, _ := edit.GetJSON(path, "defaultModel.provider"); cur == magpieID {
		if err := asideClearDefault(path, unstash(at.key("aside.was"))); err != nil {
			return err
		}
	}
	for _, role := range asideRoles {
		if p, _ := edit.GetJSON(path, "modelCategories."+role+".provider"); p != magpieID {
			continue // the user's own role, or none at all
		}
		// an empty was is magpie's own doing and nothing of the user's behind
		// it: the role goes back to following the default rather than naming a
		// provider that is no longer there
		if err := asideApplyRole(path, role, unstash(at.key("aside.was."+role))); err != nil {
			return err
		}
	}
	return nil
}
