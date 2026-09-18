package app

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/config"
	"github.com/ctrl-research/seaglass/internal/k8s"
)

// unstructuredNestedStringMap reads a string map at a dotted path.
func unstructuredNestedStringMap(o *unstructured.Unstructured, path string) (map[string]string, bool, error) {
	return unstructured.NestedStringMap(o.Object, splitDotPath(path)...)
}

func splitDotPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}

// relatedKind is what a related jump navigates to.
type relatedKind int

const (
	// relPodsBySelector opens pods matching a label selector.
	relPodsBySelector relatedKind = iota
	// relPodsOnNode opens pods scheduled on a node.
	relPodsOnNode
	// relNode opens a node's detail.
	relNode
	// relObject opens a specific object's detail, resolved by kind/group.
	relObject
	// relInventory opens the Flux inventory list for a Kustomization.
	relInventory
)

// relatedTarget is one navigable relationship of a source object.
type relatedTarget struct {
	kind      relatedKind
	label     string // picker label
	detail    string // picker detail
	namespace string
	selector  string // label selector (relPodsBySelector)
	nodeName  string // node (relPodsOnNode, relNode)
	title     string // crumb for the destination
	// relObject fields:
	targetKind  string
	targetGroup string
	targetName  string
	// relInventory:
	inventory []k8s.ObjectRef
	owner     string
}

// relatedMsg carries the related targets computed for a source object.
type relatedMsg struct {
	targets []relatedTarget
	err     error
}

// computeRelated fetches an object (unless provided) and derives its
// related targets.
func (m *Model) computeRelated(res k8s.Resource, namespace, name string, obj *unstructured.Unstructured) tea.Cmd {
	jumps := m.configJumps
	build := func(o *unstructured.Unstructured) relatedMsg {
		return relatedMsg{targets: relatedTargetsFor(res, namespace, name, o, jumps)}
	}
	if obj != nil {
		return func() tea.Msg { return build(obj) }
	}
	get := m.deps.get
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		o, err := get.Get(ctx, res, namespace, name)
		if err != nil {
			return relatedMsg{err: err}
		}
		return build(o)
	}
}

// relatedTargetsFor derives the relationships available from an object,
// built-ins first then config-driven jumps.
func relatedTargetsFor(res k8s.Resource, namespace, name string, o *unstructured.Unstructured, jumps []config.JumpRule) []relatedTarget {
	var out []relatedTarget
	kind := res.Kind

	if sel, ok := k8s.LabelSelectorString(o); ok {
		out = append(out, relatedTarget{
			kind: relPodsBySelector, label: "pods", detail: "pods matching " + sel,
			namespace: namespace, selector: sel, title: "pods of " + name,
		})
	}
	if kind == "Pod" {
		if node, ok := k8s.PodNodeName(o); ok {
			out = append(out, relatedTarget{
				kind: relNode, label: "node", detail: node, nodeName: node, title: node,
			})
		}
	}
	if kind == "Node" {
		out = append(out, relatedTarget{
			kind: relPodsOnNode, label: "pods on node", detail: "pods scheduled on " + name,
			nodeName: name, title: "pods on " + name,
		})
	}
	if k8s.IsFlux(res) {
		if inv := k8s.FluxInventory(o); len(inv) > 0 {
			out = append(out, relatedTarget{
				kind: relInventory, label: "inventory", detail: fmt.Sprintf("%d managed objects", len(inv)),
				inventory: inv, owner: res.Kind + "/" + name, title: "inventory",
			})
		}
	}
	out = append(out, configJumpTargets(res, namespace, o, jumps)...)
	return out
}

// configJumpTargets evaluates the config jump rules that match this
// resource against the object.
func configJumpTargets(res k8s.Resource, namespace string, o *unstructured.Unstructured, jumps []config.JumpRule) []relatedTarget {
	var out []relatedTarget
	for _, j := range jumps {
		if !j.Match.Matches(res.GVR.Group, res.GVR.Resource, res.Kind) {
			continue
		}
		switch {
		case j.From.Field != "":
			kind, name, ns, group, ok := k8s.NestedRef(o, j.From.Field)
			if !ok {
				continue
			}
			if ns == "" {
				ns = namespace
			}
			out = append(out, relatedTarget{
				kind: relObject, label: j.Name, detail: kind + "/" + name,
				targetKind: kind, targetGroup: group, targetName: name, namespace: ns, title: name,
			})
		case j.From.NameLabel != "":
			name, ok := k8s.Label(o, j.From.NameLabel)
			if !ok || name == "" {
				continue
			}
			ns := namespace
			if j.From.NamespaceLabel != "" {
				if v, ok := k8s.Label(o, j.From.NamespaceLabel); ok && v != "" {
					ns = v
				}
			}
			out = append(out, relatedTarget{
				kind: relObject, label: j.Name, detail: j.To.Kind + "/" + name,
				targetKind: j.To.Kind, targetGroup: j.To.Group, targetName: name, namespace: ns, title: name,
			})
		case j.From.Selector != "":
			sel, ok := selectorAtPath(o, j.From.Selector)
			if !ok {
				continue
			}
			out = append(out, relatedTarget{
				kind: relPodsBySelector, label: j.Name, detail: "pods matching " + sel,
				namespace: namespace, selector: sel, title: j.Name,
			})
		}
	}
	return out
}

// selectorAtPath reads a label-map at a dotted path and joins it.
func selectorAtPath(o *unstructured.Unstructured, path string) (string, bool) {
	m, found, _ := unstructuredNestedStringMap(o, path)
	if !found || len(m) == 0 {
		return "", false
	}
	return k8s.JoinLabelSelector(m), true
}

// handleRelated opens the picker, navigates directly for a single target,
// or reports none.
func (m Model) handleRelated(msg relatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	switch len(msg.targets) {
	case 0:
		return m, m.setNotice("no related resources")
	case 1:
		return m, m.navigateRelated(msg.targets[0])
	default:
		m.relatedTargets = msg.targets
		cmd := m.palette.showWith(relatedItems(msg.targets), "jump to…")
		m.top().resize(m.width, m.bodyHeight())
		return m, cmd
	}
}

// navigateRelated opens the destination for a related target.
func (m *Model) navigateRelated(t relatedTarget) tea.Cmd {
	switch t.kind {
	case relPodsBySelector:
		m.top().stop()
		m.nextID++
		v := newResourceView(m.nextID, k8s.Pods, t.namespace)
		v.labelSelector = t.selector
		v.title = t.title
		m.applyBadges(v)
		m.stack = append(m.stack, v)
		return m.startTop()
	case relPodsOnNode:
		m.top().stop()
		m.nextID++
		v := newResourceView(m.nextID, k8s.Pods, "")
		v.fieldSelector = "spec.nodeName=" + t.nodeName
		v.title = t.title
		m.applyBadges(v)
		m.stack = append(m.stack, v)
		return m.startTop()
	case relNode:
		node, ok := k8s.ResourceByKind(m.resources, "Node", "v1")
		if !ok {
			return m.setNotice("nodes are not a known resource")
		}
		m.top().stop()
		m.pushObject(node, "", t.nodeName, modeDetail)
		return m.startTop()
	case relInventory:
		m.top().stop()
		m.nextID++
		iv := newInventoryView(m.nextID, t.owner, t.inventory, func(kind, group string) (k8s.Resource, bool) {
			return k8s.ResourceByKindGroup(m.resources, kind, group)
		})
		m.stack = append(m.stack, iv)
		return m.startTop()
	case relObject:
		target, ok := k8s.ResourceByKindGroup(m.resources, t.targetKind, t.targetGroup)
		if !ok {
			return m.setNotice(t.targetKind + " is not a known resource")
		}
		ns := t.namespace
		if !target.Namespaced {
			ns = ""
		}
		m.top().stop()
		m.pushObject(target, ns, t.targetName, modeDetail)
		return m.startTop()
	}
	return nil
}
