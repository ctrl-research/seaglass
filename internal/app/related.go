package app

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// relatedKind is what a related jump navigates to.
type relatedKind int

const (
	// relPodsBySelector opens pods matching a label selector.
	relPodsBySelector relatedKind = iota
	// relPodsOnNode opens pods scheduled on a node.
	relPodsOnNode
	// relNode opens a node's detail.
	relNode
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
}

// relatedMsg carries the related targets computed for a source object.
type relatedMsg struct {
	targets []relatedTarget
	err     error
}

// computeRelated fetches an object (unless provided) and derives its
// related targets.
func (m *Model) computeRelated(res k8s.Resource, namespace, name string, obj *unstructured.Unstructured) tea.Cmd {
	build := func(o *unstructured.Unstructured) relatedMsg {
		return relatedMsg{targets: relatedTargetsFor(res, namespace, name, o)}
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

// relatedTargetsFor derives the relationships available from an object.
func relatedTargetsFor(res k8s.Resource, namespace, name string, o *unstructured.Unstructured) []relatedTarget {
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
	return out
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
		m.stack = append(m.stack, v)
		return m.startTop()
	case relPodsOnNode:
		m.top().stop()
		m.nextID++
		v := newResourceView(m.nextID, k8s.Pods, "")
		v.fieldSelector = "spec.nodeName=" + t.nodeName
		v.title = t.title
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
	}
	return nil
}
