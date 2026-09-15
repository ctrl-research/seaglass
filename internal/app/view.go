package app

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// streamer is the slice of k8s.Client a resource view needs.
type streamer interface {
	Stream(ctx context.Context, res k8s.Resource, namespace, fieldSelector string) <-chan k8s.Update
}

// getter is the slice of k8s.Client an object view needs.
type getter interface {
	Get(ctx context.Context, res k8s.Resource, namespace, name string) (*unstructured.Unstructured, error)
}

// clientStreamer adapts k8s.Client to streamer.
type clientStreamer struct{ c *k8s.Client }

func (s clientStreamer) Stream(ctx context.Context, res k8s.Resource, ns, fieldSelector string) <-chan k8s.Update {
	return s.c.Stream(ctx, res.GVR, ns, fieldSelector)
}

// deps is what views need from the outside world. Tests inject fakes.
type deps struct {
	stream streamer
	get    getter
	patch  patcher
	logs   logger
	exec   execer
	edit   editor
	fwd    forwarder
}

// viewStatus is what a view contributes to the status bar.
type viewStatus struct {
	count string // e.g. "9 rows"; empty to hide
	state string // e.g. "live", "yaml 40%"
	err   error
}

// view is one entry on the navigation stack.
type view interface {
	// start begins or resumes background work and returns the command that
	// delivers its first message.
	start(d deps) tea.Cmd
	// stop cancels background work; the view keeps its last content.
	stop()
	// handleKey processes a key. consumed=false lets the root treat it as
	// global (e.g. esc pops the view).
	handleKey(msg tea.KeyPressMsg, width, height int) (cmd tea.Cmd, consumed bool)
	// resize relays the space available to the view.
	resize(width, height int)
	render(width, height int) string
	// crumbs are scope segments appended after context › namespace.
	crumbs() []string
	status() viewStatus
	// hint is the key help shown in the status bar.
	hint() string
	// help lists the view's bindings for the help overlay.
	help() []helpSection
	// capturesInput is true while a text input has focus, so global keys
	// like q are typed rather than executed.
	capturesInput() bool
}
