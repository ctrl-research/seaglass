package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// editor is the slice of k8s.Client the edit flow needs.
type editor interface {
	Get(ctx context.Context, res k8s.Resource, namespace, name string) (*unstructured.Unstructured, error)
	Update(ctx context.Context, res k8s.Resource, namespace, name string, editedYAML []byte) (*unstructured.Unstructured, error)
}

type (
	// editPrepMsg carries the temp file ready to open in $EDITOR.
	editPrepMsg struct {
		tgt      target
		path     string
		original string
		err      error
	}
	// editorDoneMsg fires when $EDITOR exits.
	editorDoneMsg struct {
		tgt      target
		path     string
		original string
		err      error
	}
	// editResultMsg reports the apply outcome.
	editResultMsg struct {
		summary string
		err     error
	}
)

// prepareEdit fetches the object, renders editable YAML, and writes it to a
// temp file. It runs off the UI thread.
func prepareEdit(e editor, saveDir string, tgt target) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		obj, err := e.Get(ctx, tgt.res, tgt.namespace, tgt.name)
		if err != nil {
			return editPrepMsg{tgt: tgt, err: err}
		}
		body, err := k8s.EditableYAML(obj)
		if err != nil {
			return editPrepMsg{tgt: tgt, err: err}
		}
		banner := bannerFor(tgt)
		content := banner + body
		path := filepath.Join(saveDir, "seaglass-edit-"+sanitizeName(tgt)+".yaml")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return editPrepMsg{tgt: tgt, err: err}
		}
		return editPrepMsg{tgt: tgt, path: path, original: content}
	}
}

func bannerFor(tgt target) string {
	name := tgt.res.Kind + " " + tgt.name
	if tgt.namespace != "" {
		name = tgt.res.Kind + " " + tgt.namespace + "/" + tgt.name
	}
	return "# Editing " + name + ". Save to apply; no change aborts.\n"
}

func sanitizeName(tgt target) string {
	s := tgt.res.Name() + "-" + tgt.namespace + "-" + tgt.name
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == ':' {
			return '-'
		}
		return r
	}, s)
}

// editorCommand builds the $EDITOR invocation for a file.
func editorCommand(path string) *exec.Cmd {
	ed := os.Getenv("KUBE_EDITOR")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	// Honor editors given with flags, e.g. "code -w" or "emacs -nw".
	parts := strings.Fields(ed)
	args := append(parts[1:], path)
	return exec.Command(parts[0], args...) //nolint:gosec // user's own editor
}

// applyEdit reads the edited file, strips the banner, and updates the
// object unless nothing changed.
func applyEdit(e editor, msg editorDoneMsg) tea.Cmd {
	return func() tea.Msg {
		defer func() { _ = os.Remove(msg.path) }()
		if msg.err != nil {
			return editResultMsg{err: msg.err}
		}
		edited, err := os.ReadFile(msg.path)
		if err != nil {
			return editResultMsg{err: err}
		}
		if string(edited) == msg.original {
			return editResultMsg{summary: "edit aborted, no change"}
		}
		body := stripBanner(string(edited))
		if strings.TrimSpace(body) == "" {
			return editResultMsg{summary: "edit aborted, empty file"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := e.Update(ctx, msg.tgt.res, msg.tgt.namespace, msg.tgt.name, []byte(body)); err != nil {
			return editResultMsg{err: err}
		}
		return editResultMsg{summary: "applied edit to " + msg.tgt.String()}
	}
}

// stripBanner removes the leading comment banner seaglass added.
func stripBanner(s string) string {
	lines := strings.SplitAfter(s, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
		i++
	}
	return strings.Join(lines[i:], "")
}
