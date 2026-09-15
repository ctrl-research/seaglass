package app

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// keyMap is every seaglass binding. Help is generated from it, and M5's
// config layer will override it, so no handler may match on a raw string.
type keyMap struct {
	// Global
	Quit    key.Binding
	Palette key.Binding
	Help    key.Binding
	Back    key.Binding

	// Table view
	Filter  key.Binding
	Sort    key.Binding
	Reverse key.Binding
	Columns key.Binding
	Rollout key.Binding
	Detail  key.Binding
	YAML    key.Binding
	Edit    key.Binding
	CopyRef key.Binding
	Owner   key.Binding
	Related key.Binding

	// Object view
	ModeYAML   key.Binding
	ModeDetail key.Binding
	Copy       key.Binding
	Reload     key.Binding
	Top        key.Binding
	Bottom     key.Binding

	// Pods / shell
	Shell      key.Binding
	ShellClose key.Binding

	// Logs
	Events        key.Binding
	Logs          key.Binding
	LogContainer  key.Binding
	LogSince      key.Binding
	LogTimestamps key.Binding
	LogWrap       key.Binding
	LogFollow     key.Binding
	LogSave       key.Binding

	// Port-forwards
	Forwards key.Binding
	Delete   key.Binding

	// Palette and filter input
	Accept   key.Binding
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
}

func bind(help, desc string, keys ...string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
}

var keys = keyMap{
	Quit:    bind("q", "quit", "q", "ctrl+c"),
	Palette: bind(":", "command palette", ":", "ctrl+p"),
	Help:    bind("?", "keys", "?"),
	Back:    bind("esc", "back / close", "esc"),

	Filter:  bind("/", "filter rows", "/"),
	Sort:    bind("s", "sort by column", "s"),
	Reverse: bind("S", "reverse sort", "S"),
	Rollout: bind("R", "rollout status", "R"),
	Columns: bind("w", "cycle columns auto/wide/narrow", "w"),
	Detail:  bind("enter", "detail", "enter", "d"),
	YAML:    bind("y", "yaml", "y"),
	Edit:    bind("e", "edit in $EDITOR", "e"),
	CopyRef: bind("c", "copy name / kubectl command", "c"),
	Owner:   bind("o", "jump to owner", "o"),
	Related: bind("J", "jump to related", "J"),

	ModeYAML:   bind("y", "show yaml", "y"),
	ModeDetail: bind("d", "show detail", "d"),
	Copy:       bind("c", "copy yaml to clipboard", "c"),
	Reload:     bind("r", "reload", "r"),
	Top:        bind("g", "top", "g"),
	Bottom:     bind("G", "bottom", "G"),

	Shell:         bind("x", "shell into container", "x"),
	Events:        bind("E", "events for this object", "E"),
	ShellClose:    bind("ctrl+]", "close the shell", "ctrl+]"),
	Logs:          bind("l", "logs", "l"),
	Forwards:      bind("F", "port-forward this pod", "F"),
	Delete:        bind("ctrl+d", "cancel", "ctrl+d"),
	LogContainer:  bind("c", "choose container", "c"),
	LogSince:      bind("s", "since (time window)", "s"),
	LogTimestamps: bind("t", "toggle timestamps", "t"),
	LogWrap:       bind("w", "toggle wrap", "w"),
	LogFollow:     bind("f", "follow (jump to end)", "f"),
	LogSave:       bind("S", "save to file", "S"),

	Accept:   bind("enter", "choose", "enter"),
	Up:       bind("↑/ctrl+p", "previous", "up", "ctrl+p", "ctrl+k"),
	Down:     bind("↓/ctrl+n", "next", "down", "ctrl+n", "ctrl+j"),
	PageUp:   bind("pgup", "page up", "pgup"),
	PageDown: bind("pgdn", "page down", "pgdown"),
}

// is reports whether msg matches the binding.
func is(msg tea.KeyPressMsg, b key.Binding) bool { return key.Matches(msg, b) }

// helpSection is a titled group of bindings for the help overlay.
type helpSection struct {
	title    string
	bindings []key.Binding
}

func globalHelp() helpSection {
	return helpSection{"Global", []key.Binding{keys.Palette, keys.Help, keys.Back, keys.Quit}}
}

func paletteHelp() helpSection {
	return helpSection{"Palette / filter input", []key.Binding{keys.Up, keys.Down, keys.Accept, bind("esc", "close", "esc")}}
}
