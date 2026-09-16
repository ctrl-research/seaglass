// Package app holds the root Bubble Tea model. Models are pure with respect
// to cluster I/O: everything network-bound runs in internal/k8s goroutines or
// tea.Cmds and arrives here as messages.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/config"
	"github.com/ctrl-research/seaglass/internal/k8s"
	"github.com/ctrl-research/seaglass/internal/ui"
)

// stateStore persists UI state between runs. Nil disables persistence.
type stateStore interface {
	Load() (config.State, error)
	Save(config.State) error
}

// Options configures the root model.
type Options struct {
	Client    *k8s.Client
	Namespace string // "" means all namespaces
	Resource  k8s.Resource
	Version   string // seaglass build version for the header
	// State persists the last context, namespace, and resource. Optional.
	State stateStore
	// Ruleset is the loaded operator config (presets + user). Optional.
	Ruleset config.Ruleset

	// SaveDir is where log files are written. Defaults to the working dir.
	SaveDir string

	// streamer, getter, patcher, logger, and execer override the client
	// for tests.
	streamer  streamer
	getter    getter
	patcher   patcher
	logger    logger
	execer    execer
	editer    editor
	forwarder forwarder
	// contexts overrides kubeconfig context discovery for tests.
	contexts []string
}

// Model is the root application model: a navigation stack of resource
// views, a command palette, and the cluster connection.
type Model struct {
	client    *k8s.Client
	deps      deps
	namespace string

	stack  []view
	nextID int

	palette    palette
	resources  []k8s.Resource
	namespaces []string
	contexts   []string

	connecting     string // context name while switching, "" otherwise
	err            error
	showHelp       bool
	confirm        *confirmDialog
	prompt         prompt
	execTarget     *target         // pod awaiting a container choice for a shell
	fwdTarget      *target         // pod awaiting a port choice for a forward
	relatedTargets []relatedTarget // targets awaiting a related-jump choice
	forwards       forwards
	notice         string
	noticeSeq      int

	version       string // seaglass version
	serverVersion string // fetched from /version

	state         stateStore
	lastSaved     string // fingerprint of the last persisted position
	saveDir       string
	configActions []action

	width, height int
}

// Messages from cmds.
type (
	resourcesMsg struct {
		resources []k8s.Resource
		err       error
	}
	namespacesMsg struct {
		names []string
		err   error
	}
	contextsMsg struct {
		names []string
		err   error
	}
	clientMsg struct {
		client *k8s.Client
		err    error
	}
	versionMsg struct {
		version string
		err     error
	}
	clearNoticeMsg struct{ seq int }
	// execContainersMsg carries a pod's containers for a pending shell.
	execContainersMsg struct {
		tgt   target
		names []string
		err   error
	}
)

// New builds the root model. Streaming starts in Init.
func New(opts Options) Model {
	m := Model{
		client:        opts.Client,
		deps:          deps{stream: opts.streamer, get: opts.getter, patch: opts.patcher, logs: opts.logger, exec: opts.execer, edit: opts.editer, fwd: opts.forwarder},
		saveDir:       opts.SaveDir,
		namespace:     opts.Namespace,
		palette:       newPalette(),
		prompt:        newPrompt(),
		contexts:      opts.contexts,
		version:       opts.Version,
		state:         opts.State,
		configActions: configActionsFromRules(opts.Ruleset.Actions),
	}
	if m.deps.stream == nil && opts.Client != nil {
		m.deps.stream = clientStreamer{opts.Client}
	}
	if m.deps.get == nil && opts.Client != nil {
		m.deps.get = opts.Client
	}
	if m.deps.patch == nil && opts.Client != nil {
		m.deps.patch = opts.Client
	}
	if m.deps.logs == nil && opts.Client != nil {
		m.deps.logs = opts.Client
	}
	if m.deps.exec == nil && opts.Client != nil {
		m.deps.exec = opts.Client
	}
	if m.deps.edit == nil && opts.Client != nil {
		m.deps.edit = opts.Client
	}
	if m.deps.fwd == nil && opts.Client != nil {
		m.deps.fwd = opts.Client
	}
	if m.saveDir == "" {
		m.saveDir = "."
	}
	m.pushResource(opts.Resource)
	return m
}

// Init starts the first stream and kicks off discovery.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.top().start(m.deps)}
	cmds = append(cmds, m.discoverCmds()...)
	if m.contexts == nil {
		cmds = append(cmds, func() tea.Msg {
			names, _, err := k8s.ListContexts()
			return contextsMsg{names: names, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// discoverCmds fetches resource types and namespaces for the palette.
func (m Model) discoverCmds() []tea.Cmd {
	c := m.client
	if c == nil {
		return nil
	}
	return []tea.Cmd{
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rs, err := c.Resources(ctx)
			return resourcesMsg{resources: rs, err: err}
		},
		func() tea.Msg {
			v, err := c.ServerVersion(context.Background())
			return versionMsg{version: v, err: err}
		},
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			names, err := c.NamespaceNames(ctx)
			return namespacesMsg{names: names, err: err}
		},
	}
}

func (m *Model) top() view { return m.stack[len(m.stack)-1] }

// pushResource adds a table view for res on top of the stack. It does not
// start it.
func (m *Model) pushResource(res k8s.Resource) *resourceView {
	m.nextID++
	v := newResourceView(m.nextID, res, m.namespace)
	m.stack = append(m.stack, v)
	return v
}

// pushLogs adds a logs view for a pod. It does not start it.
func (m *Model) pushLogs(ns, pod string) *logsView {
	m.nextID++
	v := newLogsView(m.nextID, ns, pod, m.saveDir)
	m.stack = append(m.stack, v)
	return v
}

// pushObject adds an object view on top of the stack. It does not start it.
func (m *Model) pushObject(res k8s.Resource, ns, name string, mode objMode) *objectView {
	m.nextID++
	v := newObjectView(m.nextID, res, ns, name, mode)
	m.stack = append(m.stack, v)
	return v
}

// currentResource is the resource of the nearest table view, or Pods.
func (m *Model) currentResource() k8s.Resource {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if rv, ok := m.stack[i].(*resourceView); ok {
			return rv.res
		}
	}
	return k8s.Pods
}

// header describes the top block for the current state.
func (m Model) header() ui.Header {
	ns := m.namespace
	if ns == "" {
		ns = "all"
	}
	ctx, user, host := "-", "-", "-"
	if m.client != nil {
		ctx, user, host = m.client.Context, orDash(m.client.User), orDash(m.client.Host)
	}
	return ui.Header{Fields: []ui.Field{
		{Key: "context", Value: ctx},
		{Key: "namespace", Value: ns},
		{Key: "user", Value: user},
		{Key: "cluster", Value: host},
		{Key: "k8s", Value: orDash(m.serverVersion)},
		{Key: "seaglass", Value: orDash(m.version)},
	}}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// headerHeight is the lines the header takes, 0 when hidden. The help
// overlay reclaims the space.
func (m Model) headerHeight() int {
	if m.showHelp {
		return 0
	}
	if m.header().Visible(m.width, m.height) {
		return ui.HeaderHeight
	}
	return 0
}

// bodyHeight is the height available to the top view.
func (m Model) bodyHeight() int {
	return max(m.height-1-m.headerHeight()-m.palette.height()-m.prompt.height(), 1)
}

func (m *Model) rebuildPalette() {
	m.palette.setItems(buildItems(m.resources, m.namespaces, m.contexts))
}

// Update handles messages, then persists the position if it changed.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	nm := next.(Model)
	if save := nm.persist(); save != nil {
		cmd = tea.Batch(cmd, save)
	}
	return nm, cmd
}

// persist returns a command that saves the current position when it
// differs from the last save.
func (m *Model) persist() tea.Cmd {
	if m.state == nil || m.client == nil || m.connecting != "" {
		return nil
	}
	res := m.currentResource()
	key := m.client.Context + "\x00" + m.namespace + "\x00" + res.GVR.String()
	if key == m.lastSaved {
		return nil
	}
	m.lastSaved = key
	store, ctx, ns := m.state, m.client.Context, m.namespace
	return func() tea.Msg {
		st, err := store.Load()
		if err != nil {
			slog.Warn("load state", "err", err)
			st = config.State{}
		}
		st.Update(ctx, config.ContextState{Namespace: ns, AllNamespaces: ns == "", Resource: &res})
		if err := store.Save(st); err != nil {
			slog.Warn("save state", "err", err)
		}
		return nil
	}
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.palette.width = msg.Width
		m.top().resize(m.width, m.bodyHeight())
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case updateMsg:
		// Only the top view is live; anything else was popped or restarted.
		if rv, ok := m.top().(*resourceView); ok && rv.id == msg.id {
			return m, rv.handle(msg, m.width, m.bodyHeight())
		}
		return m, nil

	case objectMsg:
		if ov, ok := m.top().(*objectView); ok && ov.id == msg.id {
			ov.handle(msg)
		}
		return m, nil

	case containersMsg:
		if lv, ok := m.top().(*logsView); ok && lv.id == msg.id {
			return m, lv.handleContainers(msg)
		}
		return m, nil

	case execContainersMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		switch len(msg.names) {
		case 0:
			m.err = fmt.Errorf("%s has no containers", msg.tgt)
			return m, nil
		case 1:
			return m, m.shell(msg.tgt, msg.names[0])
		}
		tgt := msg.tgt
		m.execTarget = &tgt
		cmd := m.palette.showWith(execItems(msg.names), "shell into which container?")
		m.top().resize(m.width, m.bodyHeight())
		return m, cmd

	case shellOutputMsg:
		if sv, ok := m.top().(*shellView); ok && sv.id == msg.id {
			return m, sv.handleOutput(msg)
		}
		return m, nil

	case shellExitMsg:
		if sv, ok := m.top().(*shellView); ok && sv.id == msg.id {
			if clean := sv.handleExit(msg); clean {
				// Return to the view beneath the shell automatically.
				summary := "shell closed: " + sv.namespace + "/" + sv.pod + " [" + sv.container + "]"
				return m, tea.Batch(m.pop(), m.setNotice(summary))
			}
			// A real failure stays on screen until esc.
		}
		return m, nil

	case shellUserMsg:
		if sv, ok := m.top().(*shellView); ok && sv.id == msg.id {
			sv.user = msg.user
		}
		return m, nil

	case ownerJumpMsg:
		return m.handleOwnerJump(msg)

	case relatedMsg:
		return m.handleRelated(msg)

	case rolloutStatusMsg:
		if rv, ok := m.top().(*rolloutView); ok && rv.id == msg.id {
			return m, rv.handleStatus(msg)
		}
		return m, nil

	case rolloutTickMsg:
		if rv, ok := m.top().(*rolloutView); ok && rv.id == msg.id {
			return m, rv.handleTick(msg)
		}
		return m, nil

	case spinner.TickMsg:
		if rv, ok := m.top().(*rolloutView); ok {
			return m, rv.handleSpinner(msg)
		}
		return m, nil

	case editPrepMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		ready := msg
		return m, tea.ExecProcess(editorCommand(msg.path), func(err error) tea.Msg {
			return editorDoneMsg{tgt: ready.tgt, path: ready.path, original: ready.original, err: err}
		})

	case editorDoneMsg:
		return m, applyEdit(m.deps.edit, msg)

	case editResultMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		return m, m.setNotice(msg.summary)

	case fwdContainersMsg:
		return m.handleFwdContainers(msg)

	case forwardStartedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.forwards.nextID++
		msg.af.id = m.forwards.nextID
		m.forwards.add(msg.af)
		return m, tea.Batch(
			watchForward(msg.af),
			m.setNotice(fmt.Sprintf("forwarding %s → %s:%d", msg.af.pf.Addr(), msg.af.label, msg.af.remote)),
		)

	case forwardDiedMsg:
		if af := m.forwards.remove(msg.id); af != nil {
			if msg.err != nil {
				m.err = fmt.Errorf("port-forward %s stopped: %w", af.label, msg.err)
			}
		}
		return m, nil

	case forwardStoppedMsg:
		m.forwards.remove(msg.id)
		return m, m.setNotice("stopped forward " + msg.addr)

	case logLinesMsg:
		if lv, ok := m.top().(*logsView); ok && lv.id == msg.id {
			return m, lv.handleLines(msg)
		}
		return m, nil

	case resourcesMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.resources = msg.resources
		}
		m.rebuildPalette()
		return m, nil

	case namespacesMsg:
		if msg.err == nil {
			m.namespaces = msg.names
		}
		m.rebuildPalette()
		return m, nil

	case contextsMsg:
		if msg.err == nil {
			m.contexts = msg.names
		}
		m.rebuildPalette()
		return m, nil

	case clientMsg:
		m.connecting = ""
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		return m, m.useClient(msg.client)

	case versionMsg:
		if msg.err == nil {
			m.serverVersion = msg.version
		}
		return m, nil

	case actionResultMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		notice := m.setNotice(msg.summary)
		if msg.follow && k8s.Rolloutable(msg.tgt.res) {
			return m, tea.Batch(notice, m.openRollout(msg.tgt))
		}
		return m, notice

	case clearNoticeMsg:
		if msg.seq == m.noticeSeq {
			m.notice = ""
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	slog.Debug("key", "key", msg.String(), "palette", m.palette.open, "help", m.showHelp, "stack", len(m.stack))
	if m.showHelp {
		if is(msg, keys.Help) || is(msg, keys.Back) || is(msg, keys.Quit) {
			m.showHelp = false
		}
		return m, nil
	}
	if m.confirm != nil {
		switch msg.String() {
		case "y", "Y":
			d := m.confirm
			m.confirm = nil
			return m, d.run(d.force)
		case "f", "F":
			if m.confirm.hasForce {
				m.confirm.force = !m.confirm.force
			}
		case "n", "N", "esc", "q", "ctrl+c":
			m.confirm = nil
		}
		return m, nil
	}
	if m.prompt.open {
		value, submitted, closed, cmd := m.prompt.update(msg)
		if closed {
			m.top().resize(m.width, m.bodyHeight())
		}
		if submitted {
			p := m.prompt.pending
			p.input = value
			// A port-forward prompt (pod with no declared ports) starts a
			// forward rather than patching.
			if m.fwdTarget != nil && p.act.Name == "port-forward" {
				tgt := *m.fwdTarget
				m.fwdTarget = nil
				port, _ := strconv.Atoi(value)
				return m, tea.Batch(cmd, startForward(m.deps.fwd, tgt.res, tgt.namespace, tgt.name, 0, uint16(port)))
			}
			if p.act.Confirm {
				m.confirm = actionConfirm(m.deps, p)
				return m, cmd
			}
			return m, tea.Batch(cmd, runAction(m.deps, p))
		}
		return m, cmd
	}
	m.err = nil
	if m.palette.open {
		chosen, closed, cmd := m.palette.update(msg)
		if closed {
			m.top().resize(m.width, m.bodyHeight())
			if chosen == nil {
				m.execTarget = nil
				m.fwdTarget = nil
				m.relatedTargets = nil
			}
		}
		if chosen != nil {
			return m, tea.Batch(cmd, m.choose(*chosen))
		}
		return m, cmd
	}

	top := m.top()
	if top.capturesInput() {
		cmd, _ := top.handleKey(msg, m.width, m.bodyHeight())
		return m, cmd
	}

	switch {
	case is(msg, keys.Quit):
		for _, v := range m.stack {
			v.stop()
		}
		return m, tea.Quit
	case is(msg, keys.Palette):
		var extra []paletteItem
		if rv, ok := top.(*resourceView); ok {
			if row, ok := rv.selectedRow(); ok {
				extra = actionItems(rv.res, row, m.configActions)
			}
		}
		cmd := m.palette.showExtra(extra)
		top.resize(m.width, m.bodyHeight())
		return m, cmd
	case is(msg, keys.Help):
		m.showHelp = true
		return m, nil
	}

	// Actions and drill-down on the selected row of a table view.
	if rv, ok := top.(*resourceView); ok {
		for _, a := range actionsFor(rv.res, m.configActions) {
			if is(msg, a.Key) {
				return m, m.trigger(a, rv)
			}
		}
		switch {
		case is(msg, keys.Forwards):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			if rv.res.GVR != k8s.Pods.GVR {
				return m, m.setNotice("port-forward opens from a pod; select one in the pods view")
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			tgt := target{res: rv.res, namespace: ns, name: row.Name}
			get := m.deps.get
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				obj, err := get.Get(ctx, k8s.Pods, tgt.namespace, tgt.name)
				if err != nil {
					return fwdContainersMsg{tgt: tgt, err: err}
				}
				return fwdContainersMsg{tgt: tgt, ports: k8s.ContainerPorts(obj)}
			}
		case is(msg, keys.Shell):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			if rv.res.GVR != k8s.Pods.GVR {
				return m, m.setNotice("shell opens from a pod; select one in the pods view")
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			tgt := target{res: rv.res, namespace: ns, name: row.Name}
			get := m.deps.get
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				obj, err := get.Get(ctx, k8s.Pods, tgt.namespace, tgt.name)
				if err != nil {
					return execContainersMsg{tgt: tgt, err: err}
				}
				return execContainersMsg{tgt: tgt, names: k8s.Containers(obj)}
			}
		case is(msg, keys.Events):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			rv.stop()
			m.pushEvents(rv.res, ns, row)
			return m, m.startTop()
		case is(msg, keys.Logs):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			if rv.res.GVR != k8s.Pods.GVR {
				return m, m.setNotice("logs open from a pod; select one in the pods view")
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			rv.stop()
			m.pushLogs(ns, row.Name)
			return m, m.startTop()
		case is(msg, keys.Rollout):
			row, ok := rv.selectedRow()
			if !ok || !k8s.Rolloutable(rv.res) {
				return m, nil
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			rv.stop()
			return m, m.openRollout(target{res: rv.res, namespace: ns, name: row.Name})
		case is(msg, keys.Sort):
			if len(rv.snapshot.Columns) == 0 {
				return m, nil
			}
			cmd := m.palette.showWith(sortItems(rv.snapshot.Columns, rv.sortCol), "sort by column")
			rv.resize(m.width, m.bodyHeight())
			return m, cmd
		case is(msg, keys.Edit):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			return m, prepareEdit(m.deps.edit, m.saveDir, target{res: rv.res, namespace: ns, name: row.Name})
		case is(msg, keys.Owner):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			return m, m.jumpToOwner(rv.res, ns, row.Name, nil)
		case is(msg, keys.Related):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			return m, m.computeRelated(rv.res, ns, row.Name, nil)
		case is(msg, keys.CopyRef):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			ctx := ""
			if m.client != nil {
				ctx = m.client.Context
			}
			cmd := m.palette.showWith(copyItems(rv.res, ns, row.Name, ctx), "copy…")
			rv.resize(m.width, m.bodyHeight())
			return m, cmd
		case is(msg, keys.Detail), is(msg, keys.YAML):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			mode := modeDetail
			if is(msg, keys.YAML) {
				mode = modeYAML
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			rv.stop()
			m.pushObject(rv.res, ns, row.Name, mode)
			return m, m.startTop()
		}
	}
	if ov, ok := top.(*objectView); ok {
		switch {
		case is(msg, keys.Reload):
			return m, ov.start(m.deps)
		case is(msg, keys.Edit):
			return m, prepareEdit(m.deps.edit, m.saveDir, target{res: ov.res, namespace: ov.namespace, name: ov.name})
		case is(msg, keys.Owner):
			return m, m.jumpToOwner(ov.res, ov.namespace, ov.name, ov.obj)
		case is(msg, keys.Related):
			return m, m.computeRelated(ov.res, ov.namespace, ov.name, ov.obj)
		}
	}
	if fv, ok := top.(*forwardsView); ok {
		switch {
		case is(msg, keys.Delete), is(msg, keys.Accept):
			if af := fv.selected(); af != nil {
				m.confirm = cancelForwardConfirm(af)
			}
			return m, nil
		}
	}
	if lv, ok := top.(*logsView); ok {
		switch {
		case is(msg, keys.LogContainer):
			if len(lv.containers) == 0 {
				return m, nil
			}
			cmd := m.palette.showWith(containerItems(lv.containers, lv.selected), "container")
			lv.resize(m.width, m.bodyHeight())
			return m, cmd
		case is(msg, keys.LogSince):
			cmd := m.palette.showWith(sinceItems(lv.opts.Since), "since")
			lv.resize(m.width, m.bodyHeight())
			return m, cmd
		case is(msg, keys.LogSave):
			path, err := lv.save()
			if err != nil {
				m.err = err
				return m, nil
			}
			return m, m.setNotice("saved " + path)
		}
	}

	// The view gets first refusal (a table uses esc to clear its filter).
	cmd, consumed := top.handleKey(msg, m.width, m.bodyHeight())
	if consumed {
		return m, cmd
	}
	if is(msg, keys.Back) {
		if len(m.stack) > 1 {
			return m, m.pop()
		}
		m.err = nil
	}
	return m, nil
}

// trigger runs an action on the table's selected row, via a confirm dialog
// when the action asks for one.
func (m *Model) trigger(a action, rv *resourceView) tea.Cmd {
	row, ok := rv.selectedRow()
	if !ok {
		return nil
	}
	ns := row.Namespace
	if ns == "" {
		ns = rv.namespace
	}
	tgt := target{res: rv.res, namespace: ns, name: row.Name}
	pa := pendingAction{act: a, tgt: tgt, cols: rv.snapshot.Columns, row: row}
	if a.Input != nil {
		initial := ""
		if a.Input.Default != nil {
			initial = a.Input.Default(rv.snapshot.Columns, row)
		}
		cmd := m.prompt.show(pa, initial)
		rv.resize(m.width, m.bodyHeight())
		return cmd
	}
	if a.Confirm {
		m.confirm = actionConfirm(m.deps, pa)
		return nil
	}
	return runAction(m.deps, pa)
}

// shell pushes an embedded shell view for the container.
func (m *Model) shell(tgt target, container string) tea.Cmd {
	m.top().stop()
	m.nextID++
	v := newShellView(m.nextID, tgt.namespace, tgt.name, container)
	m.stack = append(m.stack, v)
	return m.startTop()
}

// openRollout pushes a rollout-status view and starts it, stopping the
// view beneath so it does not keep streaming while hidden.
func (m *Model) openRollout(tgt target) tea.Cmd {
	m.top().stop()
	m.nextID++
	v := newRolloutView(m.nextID, tgt.res, tgt.namespace, tgt.name)
	m.stack = append(m.stack, v)
	v.resize(m.width, m.bodyHeight())
	return v.start(m.deps)
}

// setNotice shows a transient success message for a few seconds.
func (m *Model) setNotice(text string) tea.Cmd {
	m.notice = text
	m.noticeSeq++
	seq := m.noticeSeq
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return clearNoticeMsg{seq: seq} })
}

// helpSections assembles the overlay: global, then the top view's, then
// the palette's.
func (m Model) helpSections() []ui.HelpSection {
	secs := append([]helpSection{globalHelp()}, m.top().help()...)
	if rv, ok := m.top().(*resourceView); ok {
		if acts := actionsFor(rv.res, m.configActions); len(acts) > 0 {
			bs := make([]key.Binding, 0, len(acts))
			for _, a := range acts {
				bs = append(bs, a.Key)
			}
			secs = append(secs, helpSection{"Actions on " + rv.res.Kind, bs})
		}
	}
	secs = append(secs, paletteHelp())
	out := make([]ui.HelpSection, len(secs))
	for i, s := range secs {
		out[i] = ui.HelpSection{Title: s.title, Bindings: s.bindings}
	}
	return out
}

// choose acts on a palette selection.
func (m *Model) choose(it paletteItem) tea.Cmd {
	switch it.Kind {
	case itemExec:
		if m.execTarget == nil {
			return nil
		}
		tgt := *m.execTarget
		m.execTarget = nil
		return m.shell(tgt, it.Name)
	case itemContainer:
		if lv, ok := m.top().(*logsView); ok {
			return lv.setContainer(it.Name)
		}
		return nil
	case itemPort:
		if m.fwdTarget == nil {
			return nil
		}
		tgt := *m.fwdTarget
		m.fwdTarget = nil
		return startForward(m.deps.fwd, tgt.res, tgt.namespace, tgt.name, 0, uint16(it.Index))
	case itemCopy:
		return tea.Batch(tea.SetClipboard(it.Text), m.setNotice("copied: "+it.Text))
	case itemRelated:
		if it.Index < 0 || it.Index >= len(m.relatedTargets) {
			return nil
		}
		t := m.relatedTargets[it.Index]
		m.relatedTargets = nil
		return m.navigateRelated(t)
	case itemSince:
		if lv, ok := m.top().(*logsView); ok {
			return lv.setSince(it.Since)
		}
		return nil
	case itemAction:
		switch it.Name {
		case actionQuit:
			for _, v := range m.stack {
				v.stop()
			}
			return tea.Quit
		case actionHelp:
			m.showHelp = true
		case actionForwards:
			m.openForwards()
		case actionEvents:
			return m.openClusterEvents()
		default:
			if name, ok := strings.CutPrefix(it.Name, "act:"); ok {
				if a, found := actionByName(name, m.configActions); found {
					if rv, isTable := m.top().(*resourceView); isTable {
						return m.trigger(a, rv)
					}
				}
			}
		}
		return nil
	case itemSort:
		if rv, ok := m.top().(*resourceView); ok {
			rv.setSort(it.Index)
			rv.refilter(m.width, m.bodyHeight())
		}
		return nil
	case itemResource:
		if _, isTable := m.top().(*resourceView); isTable && it.Resource.GVR == m.currentResource().GVR {
			return nil
		}
		m.top().stop()
		m.pushResource(it.Resource)
		return m.startTop()
	case itemNamespace:
		return m.setNamespace(it.Name)
	case itemContext:
		if m.client != nil && it.Name == m.client.Context {
			return nil
		}
		m.connecting = it.Name
		name := it.Name
		return func() tea.Msg {
			c, err := k8s.New(name, "")
			return clientMsg{client: c, err: err}
		}
	}
	return nil
}

// startTop sizes and starts the top view.
func (m *Model) startTop() tea.Cmd {
	v := m.top()
	v.resize(m.width, m.bodyHeight())
	return v.start(m.deps)
}

// pop removes the top view and resumes the one beneath it.
func (m *Model) pop() tea.Cmd {
	m.top().stop()
	m.stack = m.stack[:len(m.stack)-1]
	return m.startTop()
}

// setNamespace switches namespace, keeping the current resource and
// resetting the stack.
func (m *Model) setNamespace(ns string) tea.Cmd {
	if ns == m.namespace {
		return nil
	}
	m.namespace = ns
	res := m.currentResource()
	for _, v := range m.stack {
		v.stop()
	}
	m.stack = nil
	m.pushResource(res)
	return m.startTop()
}

// useClient swaps in a new cluster connection and resets everything.
func (m *Model) useClient(c *k8s.Client) tea.Cmd {
	for _, v := range m.stack {
		v.stop()
	}
	m.client = c
	// Existing forwards belong to the old cluster; tear them down.
	for _, af := range m.forwards.list {
		if af.cancel != nil {
			af.cancel()
		}
		af.pf.Stop()
	}
	m.forwards = forwards{}
	m.deps = deps{stream: clientStreamer{c}, get: c, patch: c, logs: c, exec: c, edit: c, fwd: c}
	m.namespace = c.Namespace
	m.resources, m.namespaces = nil, nil
	m.serverVersion = ""
	m.err = nil
	res := m.currentResource()
	m.stack = nil
	m.rebuildPalette()
	m.pushResource(res)
	cmds := append([]tea.Cmd{m.startTop()}, m.discoverCmds()...)
	return tea.Batch(cmds...)
}

// View renders the palette (when open), the top view, and the status bar.
func (m Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	if m.width == 0 {
		return v
	}
	top := m.top()

	// Breadcrumbs describe scope, large to small: context › namespace ›
	// resource › object. The view stack is history, not scope, so it is not
	// shown; the hint names where esc goes instead.
	// Keep the hint short; the bar drops it when it does not fit, keeping
	// the back target longer.
	back := ""
	if len(m.stack) > 1 {
		c := m.stack[len(m.stack)-2].crumbs()
		back = "esc back to " + c[len(c)-1]
	}
	st := top.status()
	bar := ui.StatusBar{
		Namespace: m.namespace,
		Crumbs:    top.crumbs(),
		Count:     st.count,
		Forwards:  m.forwards.count(),
		State:     st.state,
		Hint:      top.hint(),
		Back:      back,
	}
	if m.client != nil {
		bar.Context = m.client.Context
	}
	switch {
	case m.connecting != "":
		bar.State = "connecting to " + m.connecting
	case m.err != nil:
		bar.Err = m.err.Error()
	case st.err != nil:
		bar.Err = st.err.Error()
	case m.notice != "":
		bar.Notice = m.notice
	}

	parts := []string{}
	if m.headerHeight() > 0 {
		parts = append(parts, m.header().Render(m.width))
	}
	if m.palette.open {
		parts = append(parts, m.palette.view(m.width))
	}
	if m.prompt.open {
		parts = append(parts, m.prompt.view(m.width))
	}
	switch {
	case m.confirm != nil:
		d := m.confirm
		bar.Hint, bar.Back = "y confirm  n cancel", ""
		var toggles []ui.Toggle
		if d.hasForce {
			bar.Hint = "y confirm  f force  n cancel"
			toggles = append(toggles, ui.Toggle{Key: "f", Label: d.forceLabel, On: d.force})
		}
		parts = append(parts, ui.Confirm(d.title, d.detail, toggles, m.width, m.bodyHeight()))
	case m.showHelp:
		bar.Hint, bar.Back = "? or esc closes help", ""
		parts = append(parts, ui.RenderHelp(m.helpSections(), m.width, m.bodyHeight()))
	default:
		parts = append(parts, top.render(m.width, m.bodyHeight()))
	}
	parts = append(parts, bar.Render(m.width))
	v.SetContent(lipgloss.JoinVertical(lipgloss.Left, parts...))
	if sv, ok := top.(*shellView); ok && m.confirm == nil && !m.showHelp && !m.palette.open {
		if pos := sv.cursor(); pos != nil {
			v.Cursor = tea.NewCursor(pos.X, pos.Y+m.headerHeight()+m.prompt.height())
		}
	}
	if m.client != nil {
		v.WindowTitle = "seaglass · " + m.client.Context
	}
	return v
}
