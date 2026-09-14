package app

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// prompt is a one-line input shown above the body for actions that need a
// value. Enter submits, esc cancels.
type prompt struct {
	open    bool
	input   textinput.Model
	pending pendingAction
	errText string
}

var (
	promptTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	promptErrStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	promptHintStyle  = lipgloss.NewStyle().Faint(true)
)

func newPrompt() prompt {
	ti := textinput.New()
	ti.Prompt = ""
	ti.SetVirtualCursor(true)
	return prompt{input: ti}
}

// show opens the prompt for an action with an initial value.
func (p *prompt) show(pa pendingAction, initial string) tea.Cmd {
	p.open = true
	p.pending = pa
	p.errText = ""
	p.input.SetValue(initial)
	p.input.CursorEnd()
	p.input.Placeholder = pa.act.Input.Label
	return p.input.Focus()
}

func (p *prompt) hide() {
	p.open = false
	p.input.Blur()
	p.input.Reset()
}

// update handles a key. It returns submitted=true with the value when the
// user accepted valid input, and closed=true when the prompt went away.
func (p *prompt) update(msg tea.KeyPressMsg) (value string, submitted, closed bool, cmd tea.Cmd) {
	switch {
	case is(msg, keys.Back):
		p.hide()
		return "", false, true, nil
	case is(msg, keys.Accept):
		v := strings.TrimSpace(p.input.Value())
		if p.pending.act.Input.Validate != nil {
			if err := p.pending.act.Input.Validate(v); err != nil {
				p.errText = err.Error()
				return "", false, false, nil
			}
		}
		p.hide()
		return v, true, true, nil
	}
	p.errText = ""
	p.input, cmd = p.input.Update(msg)
	return "", false, false, cmd
}

func (p *prompt) height() int {
	if !p.open {
		return 0
	}
	return 1
}

// view renders the prompt line.
func (p *prompt) view(width int) string {
	title := promptTitleStyle.Render(" " + p.pending.act.Name + " " + p.pending.tgt.String() + " ")
	label := promptHintStyle.Render(p.pending.act.Input.Label + ": ")
	right := promptHintStyle.Render(" enter apply · esc cancel ")
	if p.errText != "" {
		right = promptErrStyle.Render(" " + p.errText + " ")
	}
	p.input.SetWidth(max(width-lipgloss.Width(title)-lipgloss.Width(label)-lipgloss.Width(right)-2, 4))
	line := title + label + p.input.View()
	gap := max(width-lipgloss.Width(line)-lipgloss.Width(right), 0)
	return lipgloss.NewStyle().MaxWidth(width).Render(line + strings.Repeat(" ", gap) + right)
}
