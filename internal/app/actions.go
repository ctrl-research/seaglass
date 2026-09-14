package app

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// patcher is the slice of k8s.Client actions need. Tests inject a fake.
type patcher interface {
	Patch(ctx context.Context, res k8s.Resource, namespace, name string, patch []byte) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, res k8s.Resource, namespace, name string, gracePeriod *int64) error
}

// action is one operation on a selected object. Built-ins are defined in
// Go here; M4's config layer will produce the same struct from YAML.
type action struct {
	Name    string      // palette label, e.g. "restart"
	Desc    string      // e.g. "rollout restart"
	Key     key.Binding // may be unbound
	Confirm bool
	// Match decides whether the action applies to a resource type.
	Match func(res k8s.Resource) bool
	// Fields builds the merge patch; nil means the action is a delete.
	Fields func(row k8s.Row, now time.Time) []k8s.Field
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
}

// pendingAction is an action awaiting confirmation.
type pendingAction struct {
	act action
	tgt target
	row k8s.Row
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
		Confirm: false,
		Match:   matchKinds("apps/deployments", "apps/statefulsets", "apps/daemonsets"),
		Fields: func(_ k8s.Row, now time.Time) []k8s.Field {
			return []k8s.Field{{
				Path:  []string{"spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt"},
				Value: now.UTC().Format(time.RFC3339),
			}}
		},
	},
	{
		Name:    "delete",
		Desc:    "delete object",
		Key:     bind("ctrl+d", "delete", "ctrl+d"),
		Confirm: true,
		Match:   func(k8s.Resource) bool { return true },
	},
}

// actionsFor returns the actions applicable to a resource type.
func actionsFor(res k8s.Resource) []action {
	var out []action
	for _, a := range builtinActions {
		if a.Match(res) {
			out = append(out, a)
		}
	}
	return out
}

// runAction executes an action against a target off the UI thread.
func runAction(p patcher, a action, tgt target, row k8s.Row) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if a.Fields == nil {
			if err := p.Delete(ctx, tgt.res, tgt.namespace, tgt.name, nil); err != nil {
				return actionResultMsg{err: err}
			}
			return actionResultMsg{summary: "deleted " + tgt.String()}
		}
		patch, err := k8s.MergePatch(a.Fields(row, time.Now()))
		if err != nil {
			return actionResultMsg{err: err}
		}
		if _, err := p.Patch(ctx, tgt.res, tgt.namespace, tgt.name, patch); err != nil {
			return actionResultMsg{err: err}
		}
		return actionResultMsg{summary: a.Desc + ": " + tgt.String()}
	}
}

// actionItems builds palette entries for the actions applicable to a row.
func actionItems(res k8s.Resource, row k8s.Row) []paletteItem {
	acts := actionsFor(res)
	items := make([]paletteItem, 0, len(acts))
	for _, a := range acts {
		detail := a.Desc + " · " + strings.TrimSpace(row.Name)
		items = append(items, paletteItem{Kind: itemAction, Label: a.Name, Detail: detail, Name: "act:" + a.Name, search: strings.ToLower(a.Name + " " + a.Desc + " action")})
	}
	return items
}

func actionByName(name string) (action, bool) {
	for _, a := range builtinActions {
		if a.Name == name {
			return a, true
		}
	}
	return action{}, false
}
