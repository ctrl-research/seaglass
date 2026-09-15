package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// eventsView builds a resourceView over the core events resource, filtered
// to one object or cluster-wide, with warning rows highlighted and a
// default sort by last-seen.
func newEventsResourceView(id int, namespace, title, fieldSelector string) *resourceView {
	v := newResourceView(id, k8s.Events, namespace)
	v.fieldSelector = fieldSelector
	v.title = title
	// Events tables put Type in a column; warn rows are Type=Warning.
	// warnCol is resolved once columns arrive, in layout.
	v.warnCol = -1
	return v
}

// pushEvents opens events scoped to one object via involvedObject.uid (or
// name when the row has no UID).
func (m *Model) pushEvents(res k8s.Resource, namespace string, row k8s.Row) *resourceView {
	sel := "involvedObject.name=" + row.Name
	if row.UID != "" {
		sel = "involvedObject.uid=" + row.UID
	}
	m.nextID++
	title := "events: " + row.Name
	v := newEventsResourceView(m.nextID, namespace, title, sel)
	m.stack = append(m.stack, v)
	return v
}

// openClusterEvents opens a cluster-wide events stream in the current
// namespace scope.
func (m *Model) openClusterEvents() tea.Cmd {
	m.top().stop()
	m.nextID++
	v := newEventsResourceView(m.nextID, m.namespace, "events", "")
	m.stack = append(m.stack, v)
	return m.startTop()
}

// resolveWarnCol finds the Type column so warning rows can be styled.
func resolveWarnCol(cols []k8s.Column) int {
	for i, c := range cols {
		if strings.EqualFold(c.Name, "Type") {
			return i
		}
	}
	return -1
}
