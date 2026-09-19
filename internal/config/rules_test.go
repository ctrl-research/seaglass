package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		m                     Match
		group, resource, kind string
		want                  bool
	}{
		{Match{Group: "apps"}, "apps", "deployments", "Deployment", true},
		{Match{Group: "apps", Kind: "Deployment"}, "apps", "deployments", "Deployment", true},
		{Match{Group: "apps", Kind: "StatefulSet"}, "apps", "deployments", "Deployment", false},
		{Match{Group: "*.toolkit.fluxcd.io"}, "kustomize.toolkit.fluxcd.io", "kustomizations", "Kustomization", true},
		{Match{Group: "*.toolkit.fluxcd.io"}, "apps", "deployments", "Deployment", false},
		{Match{}, "apps", "deployments", "Deployment", true},
		{Match{Resource: "pods"}, "", "pods", "Pod", true},
	}
	for _, c := range cases {
		if got := c.m.Matches(c.group, c.resource, c.kind); got != c.want {
			t.Errorf("%+v.Matches(%q,%q,%q) = %v, want %v", c.m, c.group, c.resource, c.kind, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	bad := Ruleset{
		Actions: []ActionRule{
			{Name: "", Match: Match{}}, // no name, empty match, no patch/delete
			{Name: "both", Match: Match{Kind: "X"}, Delete: true, Patch: map[string]any{"a": 1}},
		},
		Jumps:  []JumpRule{{Name: "j", From: JumpFrom{}}}, // no from
		Badges: []BadgeRule{{Name: "b", When: Predicate{Condition: "Ready", Status: "True"}, Style: "purple"}},
	}
	err := bad.Validate("test")
	if err == nil {
		t.Fatal("expected validation errors")
	}
	msg := err.Error()
	for _, want := range []string{"name is required", "match must set", "patch or delete", "cannot set both", "exactly one of", "must be one of"} {
		if !strings.Contains(msg, want) {
			t.Errorf("validation missing %q in:\n%s", want, msg)
		}
	}

	good := Ruleset{
		Actions: []ActionRule{{Name: "restart", Match: Match{Group: "apps"}, Patch: map[string]any{"spec": 1}}},
		Badges:  []BadgeRule{{Name: "nr", Match: Match{}, When: Predicate{Condition: "Ready", Status: "False"}, Style: "error"}},
	}
	if err := good.Validate("test"); err != nil {
		t.Errorf("valid ruleset rejected: %v", err)
	}
}

func TestPresetsLoadAndValidate(t *testing.T) {
	presets, err := Presets()
	if err != nil {
		t.Fatal(err)
	}
	var flux *Preset
	for i := range presets {
		if presets[i].Name == "flux" {
			flux = &presets[i]
		}
	}
	if flux == nil {
		t.Fatal("flux preset not found")
	}
	if len(flux.Rules.Actions) < 3 {
		t.Errorf("flux should define reconcile/suspend/resume, got %d actions", len(flux.Rules.Actions))
	}
	// The reconcile action patches the requestedAt annotation and waits.
	var reconcile *ActionRule
	for i := range flux.Rules.Actions {
		if flux.Rules.Actions[i].Name == "reconcile" {
			reconcile = &flux.Rules.Actions[i]
		}
	}
	if reconcile == nil || reconcile.Wait == nil || reconcile.Wait.Timeout.Duration() == 0 {
		t.Errorf("reconcile action wrong: %+v", reconcile)
	}
}

func TestLoadMergesUserOverPresets(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "seaglass.yaml")
	mustWrite(t, user, []byte(`
actions:
  - name: my-restart
    match: {group: example.com}
    patch: {spec: {suspend: true}}
disablePresets: []
`), 0o644)
	rs, err := loadFrom(user)
	if err != nil {
		t.Fatal(err)
	}
	// Flux preset actions plus the user's one.
	names := map[string]bool{}
	for _, a := range rs.Actions {
		names[a.Name] = true
	}
	if !names["reconcile"] || !names["my-restart"] {
		t.Errorf("merged actions = %v", names)
	}
	// The user's action is last, so it wins a by-name resolution.
	if rs.Actions[len(rs.Actions)-1].Name != "my-restart" {
		t.Error("user rules should be merged last")
	}
}

func TestLoadDisablePreset(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "seaglass.yaml")
	mustWrite(t, user, []byte("disablePresets: [flux]\n"), 0o644)
	rs, err := loadFrom(user)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Actions) != 0 {
		t.Errorf("disabling flux should leave no actions, got %d", len(rs.Actions))
	}
}

func TestLoadMissingUserFileOK(t *testing.T) {
	rs, err := loadFrom(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Actions) == 0 {
		t.Error("presets should still load without a user file")
	}
}

func TestLoadInvalidUserFile(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "seaglass.yaml")
	mustWrite(t, user, []byte("actions:\n  - match: {kind: X}\n"), 0o644) // no name
	if _, err := loadFrom(user); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected a name-required error, got %v", err)
	}
}

func mustWrite(t *testing.T, path string, data []byte, _ os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLineNumbers(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "seaglass.yaml")
	// A jump with no from on line 4.
	mustWrite(t, user, []byte("actions:\n  - name: ok\n    match: {kind: X}\n    patch: {spec: {a: 1}}\njumps:\n  - name: bad\n    match: {kind: Y}\n"), 0o644)
	_, err := loadFrom(user)
	if err == nil {
		t.Fatal("expected an error for the from-less jump")
	}
	msg := err.Error()
	if !strings.Contains(msg, "seaglass.yaml:6") {
		t.Errorf("error should cite the jump's line (6):\n%s", msg)
	}
	if !strings.Contains(msg, "jump bad") || !strings.Contains(msg, "exactly one of") {
		t.Errorf("error should name the jump and reason:\n%s", msg)
	}
}
