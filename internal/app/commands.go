package app

import (
	"os"
	"os/exec"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/ctrl-research/seaglass/internal/config"
	"github.com/ctrl-research/seaglass/internal/k8s"
)

// commandDoneMsg reports the end of an external command.
type commandDoneMsg struct {
	name string
	err  error
}

// commandsFor returns the command rules that apply to a resource.
func commandsFor(res k8s.Resource, cmds []config.CommandRule) []config.CommandRule {
	var out []config.CommandRule
	for _, c := range cmds {
		if c.Match.Matches(res.GVR.Group, res.GVR.Resource, res.Kind) {
			out = append(out, c)
		}
	}
	return out
}

// commandItems builds palette entries for the commands applicable to a row.
func commandItems(res k8s.Resource, row k8s.Row, cmds []config.CommandRule) []paletteItem {
	applicable := commandsFor(res, cmds)
	items := make([]paletteItem, 0, len(applicable))
	for _, c := range applicable {
		detail := c.Desc
		if detail == "" {
			detail = strings.Join(c.Command, " ")
		}
		items = append(items, paletteItem{
			Kind:   itemAction,
			Label:  c.Name,
			Detail: detail + " · " + strings.TrimSpace(row.Name),
			Name:   "cmd:" + c.Name,
			search: strings.ToLower(c.Name + " " + detail + " command"),
		})
	}
	return items
}

func commandByName(name string, cmds []config.CommandRule) (config.CommandRule, bool) {
	for _, c := range cmds {
		if c.Name == name {
			return c, true
		}
	}
	return config.CommandRule{}, false
}

// runCommand runs an external command with the object's identity in the
// environment, pausing the TUI. It is the config layer's escape hatch.
func runCommand(c config.CommandRule, tgt target, context string) tea.Cmd {
	if len(c.Command) == 0 {
		return nil
	}
	ex := exec.Command(c.Command[0], c.Command[1:]...) //nolint:gosec // user's own command
	ex.Env = append(os.Environ(),
		"SEAGLASS_CONTEXT="+context,
		"SEAGLASS_NAMESPACE="+tgt.namespace,
		"SEAGLASS_NAME="+tgt.name,
		"SEAGLASS_KIND="+tgt.res.Kind,
		"SEAGLASS_GROUP="+tgt.res.GVR.Group,
		"SEAGLASS_RESOURCE="+tgt.res.GVR.Resource,
	)
	name := c.Name
	return tea.ExecProcess(ex, func(err error) tea.Msg {
		return commandDoneMsg{name: name, err: err}
	})
}

// commandHelp lists the command bindings for a resource, for the overlay.
func commandHelp(res k8s.Resource, cmds []config.CommandRule) []key.Binding {
	var out []key.Binding
	for _, c := range commandsFor(res, cmds) {
		k := c.Key
		if k == "" {
			k = "(palette)"
		}
		out = append(out, key.NewBinding(key.WithHelp(k, c.Name)))
	}
	return out
}
