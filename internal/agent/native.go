package agent

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/yetone/magpie/internal/edit"
)

// NativeConnection registers a provider independently of model selection.
// Its plan describes file and runtime changes without performing either.
type NativeConnection struct {
	Read       func() NativeState
	Connect    func() error
	Apply      func(key, value string) error
	Stage      func(key, value string) error
	Disconnect func() (*DisconnectPlan, error)
	Execute    func(*DisconnectPlan) error
}

type NativeState struct {
	Provider string                 `json:"provider"`
	Detail   string                 `json:"detail,omitempty"`
	Runtime  string                 `json:"runtime"`
	Fields   map[string]NativeField `json:"fields"`
}

type NativeField struct {
	Value  string `json:"value"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type PlannedFile struct {
	Path   string
	Before []byte
	After  []byte
}

type PlannedSetting struct {
	Key    string
	Before json.RawMessage
	After  json.RawMessage
}

type DisconnectPlan struct {
	Files    []PlannedFile
	Settings []PlannedSetting
}

func (p *DisconnectPlan) Preview() []FileChange {
	var changes []FileChange
	for _, f := range p.Files {
		lines := diffLines(string(f.Before), string(f.After))
		if len(lines) != 0 {
			path := f.Path
			if home, err := os.UserHomeDir(); err == nil && len(path) > len(home) && path[:len(home)+1] == home+string(os.PathSeparator) {
				path = "~" + path[len(home):]
			}
			changes = append(changes, FileChange{Path: path, Lines: lines})
		}
	}
	return changes
}

func (p *DisconnectPlan) CheckFiles() error {
	for _, f := range p.Files {
		b, err := edit.Read(f.Path)
		if err != nil {
			return err
		}
		if string(b) != string(f.Before) {
			return fmt.Errorf("%s changed since the disconnect plan was read; try again", f.Path)
		}
	}
	return nil
}
