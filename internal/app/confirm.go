package app

import (
	"strconv"

	tea "charm.land/bubbletea/v2"
)

// confirmDialog is a generic yes/no confirmation. It decouples the dialog
// from what it confirms so cluster actions and local operations (like
// cancelling a port-forward) share one path and one look.
type confirmDialog struct {
	title  string
	detail string
	// hasForce shows a force toggle; forceLabel names it.
	hasForce   bool
	forceLabel string
	force      bool
	// run executes the confirmed operation, receiving the force state.
	run func(force bool) tea.Cmd
}

// actionConfirm builds a confirmation for a cluster action.
func actionConfirm(d deps, pa pendingAction) *confirmDialog {
	detail := pa.tgt.String()
	if pa.act.Input != nil {
		detail = pa.act.Input.Label + ": " + pa.input
	}
	return &confirmDialog{
		title:      pa.question(),
		detail:     detail,
		hasForce:   pa.act.Force,
		forceLabel: "force (grace period 0)",
		run: func(force bool) tea.Cmd {
			pa.force = force
			return runAction(d, pa)
		},
	}
}

// cancelForwardConfirm builds a confirmation for cancelling a port-forward.
// The teardown and registry update run through a message so they act on the
// live model, not a captured copy.
func cancelForwardConfirm(af *activeForward) *confirmDialog {
	return &confirmDialog{
		title:  "cancel port-forward?",
		detail: af.pf.Addr() + " → " + af.label + ":" + strconv.Itoa(int(af.remote)),
		run: func(bool) tea.Cmd {
			return func() tea.Msg {
				if af.cancel != nil {
					af.cancel()
				}
				af.pf.Stop()
				return forwardStoppedMsg{id: af.id, addr: af.pf.Addr()}
			}
		},
	}
}
