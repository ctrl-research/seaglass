package app

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// ownerJumpMsg carries the resolved controller owner of a source object.
// This is the first of the M3 jump kinds; related jumps and config-driven
// jumps in M4 follow the same shape.
type ownerJumpMsg struct {
	owner     k8s.OwnerRef
	namespace string
	found     bool
	err       error
}

// jumpToOwner fetches an object and reports its controller owner. When obj
// is already loaded (object view), pass it to skip the fetch.
func (m *Model) jumpToOwner(res k8s.Resource, namespace, name string, obj *unstructured.Unstructured) tea.Cmd {
	if obj != nil {
		owner, found := k8s.ControllerOwner(obj)
		return func() tea.Msg { return ownerJumpMsg{owner: owner, namespace: namespace, found: found} }
	}
	get := m.deps.get
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		o, err := get.Get(ctx, res, namespace, name)
		if err != nil {
			return ownerJumpMsg{err: err}
		}
		owner, found := k8s.ControllerOwner(o)
		return ownerJumpMsg{owner: owner, namespace: namespace, found: found}
	}
}

// handleOwnerJump opens the owner's detail view, or reports why it cannot.
func (m Model) handleOwnerJump(msg ownerJumpMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	if !msg.found {
		return m, m.setNotice("no owner reference")
	}
	res, ok := k8s.ResourceByKind(m.resources, msg.owner.Kind, msg.owner.APIVersion)
	if !ok {
		return m, m.setNotice("owner kind " + msg.owner.Kind + " is not a known resource")
	}
	ns := msg.namespace
	if !res.Namespaced {
		ns = ""
	}
	m.top().stop()
	m.pushObject(res, ns, msg.owner.Name, modeDetail)
	return m, m.startTop()
}
