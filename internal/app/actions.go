package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/config"
	"github.com/ctrl-research/seaglass/internal/k8s"
)

// k8sUnstructured aliases the fetched object type.
type k8sUnstructured = unstructured.Unstructured

// nestedString reads a dotted path as a string.
func nestedString(u *k8sUnstructured, path string) (string, bool, error) {
	return unstructured.NestedString(u.Object, splitPath(path)...)
}

func splitPath(p string) []string { return strings.Split(p, ".") }

// conditionStatus returns the status of a named condition, or "".
func conditionStatus(u *k8sUnstructured, condType string) string {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if ok && m["type"] == condType {
			if s, ok := m["status"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// patcher is the slice of k8s.Client actions need. Tests inject a fake.
type patcher interface {
	Patch(ctx context.Context, res k8s.Resource, namespace, name string, patch []byte) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, res k8s.Resource, namespace, name string, gracePeriod *int64) error
}

// action is one operation on a selected object. Built-ins are defined in
// Go here; M4's config layer will produce the same struct from YAML.
type action struct {
	Name string      // palette label, e.g. "restart"
	Desc string      // e.g. "rollout restart"
	Key  key.Binding // may be unbound
	// Confirm asks before running. Every built-in that changes the cluster
	// sets it; config may turn it off per action.
	Confirm bool
	// Force offers a force toggle in the confirm dialog. For deletes it
	// means grace period 0.
	Force bool
	// Follow opens a rollout-status view after the action succeeds, for
	// actions that trigger a rollout.
	Follow bool
	// Input, when set, prompts the user for a value before running.
	Input *inputSpec
	// Match decides whether the action applies to a resource type.
	Match func(res k8s.Resource) bool
	// Fields builds the merge patch; nil means the action is a delete.
	Fields func(a actionArgs) ([]k8s.Field, error)
	// rule is set for config-driven actions; it fetches the object, renders
	// the patch template, applies it, and optionally waits.
	rule *config.ActionRule
}

// inputSpec describes the prompt for an action that needs a value.
type inputSpec struct {
	Label string // e.g. "replicas"
	// Default derives the initial value from the selected row, if it can.
	Default func(cols []k8s.Column, row k8s.Row) string
	// Validate rejects bad input with a message.
	Validate func(value string) error
}

// actionArgs is everything a Fields function may use.
type actionArgs struct {
	cols  []k8s.Column
	row   k8s.Row
	input string
	now   time.Time
}

// target is a concrete object an action runs against.
type target struct {
	res       k8s.Resource
	namespace string
	name      string
}

func (t target) String() string {
	if t.namespace != "" {
		return t.res.Kind + " " + t.namespace + "/" + t.name
	}
	return t.res.Kind + " " + t.name
}

// actionResultMsg reports an action's outcome.
type actionResultMsg struct {
	summary string
	err     error
	// follow and tgt ask the model to open a rollout view on success.
	follow bool
	tgt    target
}

// pendingAction is an action awaiting confirmation or input.
type pendingAction struct {
	act   action
	tgt   target
	cols  []k8s.Column
	row   k8s.Row
	input string // value from the prompt, if the action has one
	force bool   // toggled in the confirm dialog
}

// question is the confirm dialog title for a pending action.
func (p pendingAction) question() string {
	if p.act.Input != nil {
		return fmt.Sprintf("%s %s to %s %s?", p.act.Name, p.tgt, p.input, p.act.Input.Label)
	}
	return p.act.Desc + " " + p.tgt.String() + "?"
}

// matchKinds matches a set of group/resource pairs.
func matchKinds(groupResources ...string) func(k8s.Resource) bool {
	return func(r k8s.Resource) bool {
		gr := r.GVR.Group + "/" + r.GVR.Resource
		for _, want := range groupResources {
			if gr == want {
				return true
			}
		}
		return false
	}
}

// builtinActions are the actions available without configuration.
var builtinActions = []action{
	{
		Name:    "restart",
		Desc:    "rollout restart",
		Key:     bind("r", "rollout restart", "r"),
		Confirm: true,
		Follow:  true,
		Match:   matchKinds("apps/deployments", "apps/statefulsets", "apps/daemonsets"),
		Fields: func(a actionArgs) ([]k8s.Field, error) {
			return []k8s.Field{{
				Path:  []string{"spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt"},
				Value: a.now.UTC().Format(time.RFC3339),
			}}, nil
		},
	},
	{
		Name:    "scale",
		Desc:    "scale replicas",
		Key:     bind("=", "scale to…", "="),
		Confirm: true,
		Match:   scalable,
		Input: &inputSpec{
			Label: "replicas",
			Default: func(cols []k8s.Column, row k8s.Row) string {
				n, ok := desiredReplicas(cols, row)
				if ok {
					return strconv.Itoa(n)
				}
				return ""
			},
			Validate: validateReplicas,
		},
		Fields: func(a actionArgs) ([]k8s.Field, error) {
			n, err := strconv.Atoi(strings.TrimSpace(a.input))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("replicas must be a non-negative integer")
			}
			return []k8s.Field{{Path: []string{"spec", "replicas"}, Value: n}}, nil
		},
	},
	{
		Name:    "scale up",
		Desc:    "one more replica",
		Key:     bind("+", "scale +1", "+"),
		Confirm: true,
		Match:   scalable,
		Fields:  scaleBy(1),
	},
	{
		Name:    "scale down",
		Desc:    "one fewer replica",
		Key:     bind("-", "scale -1", "-"),
		Confirm: true,
		Match:   scalable,
		Fields:  scaleBy(-1),
	},
	{
		Name:    "delete",
		Desc:    "delete object",
		Key:     bind("ctrl+d", "delete", "ctrl+d"),
		Confirm: true,
		Force:   true,
		Match:   func(k8s.Resource) bool { return true },
	},
}

var scalable = matchKinds("apps/deployments", "apps/statefulsets", "apps/replicasets")

func validateReplicas(v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return fmt.Errorf("replicas must be a non-negative integer")
	}
	return nil
}

// scaleBy patches replicas relative to the desired count read from the
// table row.
func scaleBy(delta int) func(actionArgs) ([]k8s.Field, error) {
	return func(a actionArgs) ([]k8s.Field, error) {
		cur, ok := desiredReplicas(a.cols, a.row)
		if !ok {
			return nil, fmt.Errorf("cannot read desired replicas from the table; use scale instead")
		}
		n := max(cur+delta, 0)
		return []k8s.Field{{Path: []string{"spec", "replicas"}, Value: n}}, nil
	}
}

// desiredReplicas reads spec.replicas as the server renders it: the
// denominator of a Ready "x/y" cell, or a Desired cell.
func desiredReplicas(cols []k8s.Column, row k8s.Row) (int, bool) {
	for i, c := range cols {
		if i >= len(row.Cells) {
			break
		}
		switch strings.ToLower(c.Name) {
		case "ready":
			if _, den, ok := strings.Cut(row.Cells[i], "/"); ok {
				if n, err := strconv.Atoi(den); err == nil {
					return n, true
				}
			}
		case "desired":
			if n, err := strconv.Atoi(row.Cells[i]); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// configActionsFromRules converts config action rules into actions.
func configActionsFromRules(rules []config.ActionRule) []action {
	out := make([]action, 0, len(rules))
	for i := range rules {
		r := &rules[i]
		a := action{
			Name:    r.Name,
			Desc:    r.Desc,
			Confirm: r.Confirm,
			Force:   r.Force,
			Follow:  r.Follow,
			Match:   matchRule(r.Match),
			rule:    r,
		}
		if r.Key != "" {
			a.Key = bind(r.Key, r.Desc, r.Key)
		}
		if r.Input != nil {
			in := r.Input
			a.Input = &inputSpec{
				Label:   in.Label,
				Default: func([]k8s.Column, k8s.Row) string { return renderDefault(in.Default) },
			}
			if in.Pattern != "" {
				a.Input.Validate = patternValidator(in.Pattern)
			}
		}
		out = append(out, a)
	}
	return out
}

func matchRule(m config.Match) func(k8s.Resource) bool {
	return func(res k8s.Resource) bool {
		return m.Matches(res.GVR.Group, res.GVR.Resource, res.Kind)
	}
}

// renderDefault renders an input default with no object (supports {{now}}).
func renderDefault(tmpl string) string {
	if tmpl == "" {
		return ""
	}
	out, err := config.RenderString(tmpl, config.NewRenderData(nil, "", time.Now()))
	if err != nil {
		return ""
	}
	return out
}

// patternValidator validates input against a regex.
func patternValidator(pattern string) func(string) error {
	re, err := regexp.Compile(pattern)
	return func(v string) error {
		if err != nil {
			return fmt.Errorf("invalid pattern %q", pattern)
		}
		if !re.MatchString(v) {
			return fmt.Errorf("must match %s", pattern)
		}
		return nil
	}
}

// allActions is the built-in actions followed by config actions. Config
// actions come last so a same-named user rule can shadow a built-in in a
// by-name lookup.
func allActions(configActions []action) []action {
	return append(append([]action{}, builtinActions...), configActions...)
}

// actionsFor returns the actions applicable to a resource type, built-ins
// then config actions.
func actionsFor(res k8s.Resource, configActions []action) []action {
	var out []action
	for _, a := range allActions(configActions) {
		if a.Match(res) {
			out = append(out, a)
		}
	}
	return out
}

// runAction executes a pending action off the UI thread.
func runAction(d deps, pa pendingAction) tea.Cmd {
	if pa.act.rule != nil {
		return runConfigAction(d, pa)
	}
	p, a, tgt := d.patch, pa.act, pa.tgt
	args := actionArgs{cols: pa.cols, row: pa.row, input: pa.input}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if a.Fields == nil {
			var grace *int64
			summary := "deleted " + tgt.String()
			if pa.force {
				zero := int64(0)
				grace = &zero
				summary = "force " + summary
			}
			if err := p.Delete(ctx, tgt.res, tgt.namespace, tgt.name, grace); err != nil {
				return actionResultMsg{err: err}
			}
			return actionResultMsg{summary: summary}
		}
		args.now = time.Now()
		fields, err := a.Fields(args)
		if err != nil {
			return actionResultMsg{err: err}
		}
		patch, err := k8s.MergePatch(fields)
		if err != nil {
			return actionResultMsg{err: err}
		}
		if _, err := p.Patch(ctx, tgt.res, tgt.namespace, tgt.name, patch); err != nil {
			return actionResultMsg{err: err}
		}
		summary := a.Desc + ": " + tgt.String()
		if a.Input != nil {
			summary = a.Name + " " + tgt.String() + " to " + strings.TrimSpace(args.input) + " " + a.Input.Label
		}
		return actionResultMsg{summary: summary, follow: a.Follow, tgt: tgt}
	}
}

// runConfigAction fetches the object, renders the patch from the rule's
// template, applies it (or deletes), then waits for the rule's predicate.
func runConfigAction(d deps, pa pendingAction) tea.Cmd {
	rule, tgt := pa.act.rule, pa.tgt
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		obj, err := d.get.Get(ctx, tgt.res, tgt.namespace, tgt.name)
		if err != nil {
			return actionResultMsg{err: err}
		}
		if rule.Delete {
			if err := d.patch.Delete(ctx, tgt.res, tgt.namespace, tgt.name, nil); err != nil {
				return actionResultMsg{err: err}
			}
			return actionResultMsg{summary: "deleted " + tgt.String()}
		}
		now := time.Now()
		data := config.NewRenderData(obj.Object, pa.input, now)
		patchMap, err := config.RenderPatch(rule.Patch, data)
		if err != nil {
			return actionResultMsg{err: err}
		}
		patch, err := json.Marshal(patchMap)
		if err != nil {
			return actionResultMsg{err: err}
		}
		if _, err := d.patch.Patch(ctx, tgt.res, tgt.namespace, tgt.name, patch); err != nil {
			return actionResultMsg{err: err}
		}
		summary := rule.Desc + ": " + tgt.String()
		if summary == ": "+tgt.String() {
			summary = rule.Name + ": " + tgt.String()
		}
		// Wait for the predicate, if any.
		if rule.Wait != nil {
			if werr := waitForAction(d, tgt, rule.Wait, data); werr != nil {
				return actionResultMsg{err: werr}
			}
			summary += " (done)"
		}
		return actionResultMsg{summary: summary, tgt: tgt}
	}
}

// waitForAction polls the object until the rule's wait predicate holds or
// its timeout elapses.
func waitForAction(d deps, tgt target, w *config.WaitRule, data config.RenderData) error {
	timeout := w.Timeout.Duration()
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var expected string
	if w.Field != "" {
		var err error
		if expected, err = config.RenderString(w.Equals, data); err != nil {
			return err
		}
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		obj, err := d.get.Get(ctx, tgt.res, tgt.namespace, tgt.name)
		cancel()
		if err == nil && actionPredicateHolds(obj, w, expected) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: timed out waiting for %s", tgt.String(), waitDesc(w))
		}
		time.Sleep(time.Second)
	}
}

func actionPredicateHolds(obj *k8sUnstructured, w *config.WaitRule, expected string) bool {
	if w.Field != "" {
		got, _, _ := nestedString(obj, w.Field)
		return got == expected
	}
	if w.Condition != "" {
		return conditionStatus(obj, w.Condition) == w.Status
	}
	return true
}

func waitDesc(w *config.WaitRule) string {
	if w.Field != "" {
		return w.Field
	}
	return "condition " + w.Condition + "=" + w.Status
}

// actionItems builds palette entries for the actions applicable to a row.
func actionItems(res k8s.Resource, row k8s.Row, configActions []action) []paletteItem {
	acts := actionsFor(res, configActions)
	items := make([]paletteItem, 0, len(acts))
	for _, a := range acts {
		label := a.Name
		if a.Input != nil {
			label += "…"
		}
		detail := a.Desc + " · " + strings.TrimSpace(row.Name)
		items = append(items, paletteItem{Kind: itemAction, Label: label, Detail: detail, Name: "act:" + a.Name, search: strings.ToLower(a.Name + " " + a.Desc + " action")})
	}
	return items
}

func actionByName(name string, configActions []action) (action, bool) {
	all := allActions(configActions)
	// Later entries (config) win, so scan backwards.
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Name == name {
			return all[i], true
		}
	}
	return action{}, false
}
