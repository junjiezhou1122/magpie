package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// stage is an explicit offline write, separate from confirmed runtime apply.
func (c *asideConnection) stage(key, value string) error {
	asideMu.Lock()
	defer asideMu.Unlock()
	if key == "effort" || value == "" {
		return fmt.Errorf("select a model to save for Aside's next start")
	}
	if key != "model" && key != "image" {
		valid := false
		for _, role := range asideRoles {
			if role == key {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("unknown Aside field %q", key)
		}
	}
	p, m, ok := strings.Cut(value, "/")
	if !ok || p == "" || m == "" {
		return fmt.Errorf("expected provider/model")
	}
	if key == "image" && p == "magpie" && !asideImageOffered(value) {
		return fmt.Errorf("%s is not an image generation model", value)
	}
	b, err := edit.Read(c.path)
	if err != nil {
		return err
	}
	var saved map[string]json.RawMessage
	if len(b) > 0 {
		if err := json.Unmarshal(b, &saved); err != nil {
			return err
		}
	}
	path := asideFieldPath(key)
	before := asideRaw(saved, path)
	if len(before) == 0 {
		before = json.RawMessage("null")
	}
	r, err := c.load()
	if err != nil {
		return err
	}
	owned, had := r.Fields[key]
	if had && !strings.HasPrefix(asideSelection(before), "magpie/") {
		delete(r.Fields, key)
		had = false
		owned = asideOwned{}
	}
	if p == "magpie" {
		if err := c.connectLocked(); err != nil {
			return err
		}
		if !had && !strings.HasPrefix(asideSelection(before), "magpie/") {
			owned.Before = append(json.RawMessage(nil), before...)
		}
	}
	selection := map[string]any{}
	if key != "image" {
		_ = json.Unmarshal(before, &selection)
		if selection == nil {
			selection = map[string]any{}
		}
		if _, ok := selection["thinkingLevel"]; !ok {
			selection["thinkingLevel"] = "medium"
		}
	}
	selection["provider"], selection["modelId"] = p, m
	raw, _ := json.Marshal(selection)
	owned.Pending = append(json.RawMessage(nil), raw...)
	r.Fields[key] = owned
	if err := c.save(r); err != nil {
		return err
	}
	if err := edit.SetJSON(c.path, edit.KV{Path: path, Value: json.RawMessage(raw)}); err != nil {
		return err
	}
	c.read = false
	return nil
}
