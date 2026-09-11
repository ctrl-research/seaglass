# seaglass — plan

A single-cluster Kubernetes TUI in the spirit of k9s, rebuilt around a smaller
set of ideas done well. Solo project for now; every milestone ends in a build
that is usable day to day, and the test of progress is how much of k9s it
replaces.

## Positioning

"Better k9s" has to mean something concrete or it becomes a slower clone.
These are the bets. Anything not on this list is a non-goal until v0.1.

1. **One code path for every resource.** Ask the API server for `Table`
   responses (`application/json;as=Table;v=meta.k8s.io`) and render whatever
   columns come back. Pods, CRDs, and anything installed tomorrow all get
   kubectl-identical columns with zero per-resource code.
2. **Discoverable, not memorized.** A fuzzy command palette over resource
   types, actions, namespaces, and contexts. Help is generated from the
   keymap, never hand-maintained.
3. **Live by default.** Informers/watches drive every table. No polling, no
   refresh key, no stale rows.
4. **Relationship jumps.** Pod → owner → Deployment, Service → Endpoints →
   Pods, Pod → Node, anything → its Events. One key each.
5. **Logs that don't fight you.** Merged multi-container view, regex filter,
   timestamp toggle, `--since`, wrap, save-to-file.
6. **Honest failure UX.** Expired EKS/SSO exec tokens produce a readable
   message and a retry, not a wall of client-go errors.
7. **One config file.** Theme, keymap, aliases in a single `seaglass.yaml`
   with sane defaults. No skins/plugins/hotkeys/aliases split across files.
8. **Fast to open.** Lazy informers (only the resource on screen), discovery
   cached on disk, sub-second to first table.

Non-goals for now: multi-cluster views, a plugin system, metrics/pulses
dashboards, Helm, xray-style trees, mouse support, Windows.

## Stack

| Concern | Choice | Notes |
|---|---|---|
| Language | Go 1.26 | Already installed. Single static binary. |
| TUI framework | Bubble Tea + Lip Gloss + Bubbles | Check whether Bubble Tea v2 is stable at kickoff and start there if so; migrating later is painful. |
| Cluster access | client-go: dynamic client, discovery, shared informers | Auth exec plugins (aws eks get-token, SSO) work for free. |
| Table rendering | Server-side `Table` transform via dynamic client | Watch works on Tables too. |
| Fuzzy matching | sahilm/fuzzy or charmbracelet/bubbles list filter | |
| Config | YAML via koanf or plain yaml.v3 | One file, `$XDG_CONFIG_HOME/seaglass/seaglass.yaml`. |
| Logging (internal) | log/slog to a file | Never to the terminal. |
| Tests | Go testing, `teatest` for models, golden files for views | `kind` or the `colima` context for hands-on integration. |
| Lint/CI | golangci-lint, GitHub Actions on push | |
| Release | GoReleaser | Later milestone. |

## Architecture

```
cmd/seaglass/           main: flags, config load, start tea.Program
internal/app/           root model, navigation stack, keymap dispatch, palette
internal/k8s/           client factory, discovery cache, informer manager,
                        Table watch → row stream, actions (delete/scale/exec/pf)
internal/ui/            reusable views: table, detail, yaml, logs, picker,
                        status bar, help overlay, confirm dialog
internal/config/        schema, defaults, load/validate, keymap parsing
internal/theme/         Lip Gloss styles from config
docs/                   keymap reference, config reference (generated)
```

Principles:

- **Bubble Tea models are pure.** `Update` never touches the network. All
  cluster I/O happens in `internal/k8s` goroutines that emit `tea.Msg`s.
  This keeps models unit-testable without a cluster.
- **Navigation is a stack.** Every view pushes onto it; `esc` pops.
  Breadcrumbs in the status bar reflect the stack. Context/namespace changes
  reset the stack.
- **One informer manager.** Views request a `(GVR, namespace)` stream; the
  manager dedupes, starts lazily, and tears down after a grace period when
  nothing is watching. Large clusters never have every resource type
  cached.
- **Virtualized tables.** Render only visible rows. 10k pods must scroll
  without lag.
- **Long-running actions go through `tea.ExecProcess`** (exec shell, `$EDITOR`)
  or a managed goroutine with cancel (port-forward, log follow).

## Milestones

Each milestone has a dogfood gate. Do not start the next one until the gate
passes on a real cluster (homelab or an EKS dev context). Rough sizes assume
evenings and weekends; treat them as ordering, not dates.

### M0 — Skeleton (about 1 week) → `v0.0.1`

- `go mod init`, Makefile (`build`, `test`, `lint`, `run`), golangci-lint,
  GitHub Actions running tests on push.
- Load kubeconfig, honor `--context` and `--namespace` flags and the current
  context otherwise.
- Table view listing Pods in the current namespace via the `Table`
  transform. Live updates via watch. Arrow keys, `q` quits.
- Status bar: context, namespace, resource, row count.
- Internal log to file with `--debug`.

Gate: I can open it against production and see pods update live.

### M1 — Browse (about 2 weeks) → `v0.0.2`

- [x] Discovery of every list+watch resource, cached on disk for an hour.
- [x] Command palette (`:` or `ctrl+p`): fuzzy over all discovered resource
  types (short names, plurals, CRDs), namespaces, contexts. Multi-term
  queries (`ns kube`) must all match.
- [x] Namespace picker via the palette. Context picker that rebuilds the
  client and resets the stack.
- [x] Navigation stack with breadcrumbs and `esc`. Hidden views stop
  their streams; popping restarts the stream.
- [ ] "All namespaces" as a palette entry.
- [ ] Row filter (`/`) with fuzzy match and highlight.
- [ ] Detail view (`d`): describe-style summary. YAML view (`y`) with syntax
  highlighting and copy-to-clipboard.
- [ ] Column sort (`shift+letter` or a sort picker). Wide/narrow column toggle.
- [ ] Generated help overlay (`?`) from the keymap.
- [ ] Persist last context/namespace/resource between runs.

Gate: I use seaglass instead of k9s for all read-only browsing for a week.

### M2 — Operate (about 2 weeks) → `v0.0.3`

- Logs (`l`): follow, container picker or merged multi-container view,
  `--since`, timestamps toggle, regex filter, wrap toggle, save to file.
- Exec shell into a container (`s`) via `tea.ExecProcess`.
- Delete with confirm dialog (`ctrl+d`), with force/grace options.
- Scale (`shift+s`) for Deployments/StatefulSets/ReplicaSets.
- Rollout restart (`r`) and rollout status inline.
- Edit in `$EDITOR` (`e`) with server-side apply and a diff-on-conflict
  message.
- Port-forward (`shift+f`) with an active-forwards panel and cancel.
- Copy resource name / namespace / kubectl command to clipboard.

Gate: k9s is uninstalled.

### M3 — Relationships and events (about 1 week) → `v0.0.4`

- Owner jump (`o`): Pod → ReplicaSet → Deployment, Job → CronJob, etc.
- Related jump (`shift+r`): Service → Endpoints → Pods, Deployment → Pods
  by selector, Pod → Node, Node → Pods, ConfigMap/Secret → mounting Pods.
- Events view per resource (`shift+e`) and a cluster-wide events stream
  sorted by last seen with warning highlighting.
- Pod status decoration: CrashLoopBackOff, OOMKilled, ImagePullBackOff,
  pending reasons surfaced in the row and detail view.

Gate: I can diagnose a failing rollout without leaving the tool.

### M4 — Config, polish, resilience (about 1 week) → `v0.0.5`

- `seaglass.yaml`: theme (a few built-in, plus overrides), keymap
  overrides, resource aliases, default namespace/context, log defaults.
- `seaglass config init` writes a commented default file. `seaglass config
  validate` checks it.
- Persist last context/namespace/resource between runs.
- Auth failure UX: detect exec-plugin failures and API 401s, show a
  readable message with the exec command that failed, offer retry.
- Reconnect logic for watch drops with a "reconnecting" indicator.
- Narrow terminal handling: columns collapse by priority, status bar
  truncates gracefully. Test at 80x24.
- Performance pass against the largest cluster: virtualized table,
  informer memory, startup time budget (< 1s to first table on a warm
  discovery cache).

Gate: no crashes or hangs in two weeks of daily use across the EKS
contexts, homelab, and colima.

### M5 — Distribution (a few days) → `v0.1.0`

Only when it has earned it. Everything before this is `go install` only.

- GoReleaser: darwin/linux, arm64/amd64, checksums, GitHub Releases.
- Homebrew tap under the same GitHub org.
- README with a recording (vhs), quickstart, keymap table, config reference.
- Issue templates, CONTRIBUTING, license.

### Parked (after v0.1)

Multi-cluster fleet view, metrics-server integration for CPU/memory columns,
plugin/hook system, Helm releases view, RBAC "can I" view, node shell,
benchmarking, mouse support.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Bubble Tea v1/v2 churn mid-project | Decide at M0 and pin. Wrap `tea` imports behind a small package if v2 is still pre-release. |
| Large clusters exhaust memory via informers | Lazy per-(GVR, namespace) informers, Table transform (no full objects), teardown when unwatched. |
| Table transform hides fields needed for actions | Also fetch the full object on demand for detail/YAML/edit. Rows carry name/namespace/UID. |
| Exec plugin latency on every context switch | Cache clients per context for the session. Show a spinner, never block `Update`. |
| Scope creep toward k9s parity | Non-goals list above. Every feature must map to one of the eight bets or it waits. |
| Solo project stalls | Small milestones, each shippable. Dogfood gates keep motivation tied to daily use. |

## Testing strategy

- Models: table-driven tests on `Update` with synthetic `tea.Msg`s. No
  cluster needed.
- Views: golden-file tests of rendered output at a few terminal sizes.
- k8s package: fake dynamic client for informer manager and actions; one
  opt-in integration test suite against `kind` (`make test-integration`).
- Manual: a `make demo` target that spins up `kind`, applies a fixture set
  (healthy, crashlooping, pending, OOMKilled pods; a CRD), and launches
  seaglass.

## First session checklist

1. Decide Bubble Tea version, `go mod init github.com/ctrl-research/seaglass`.
2. Makefile, golangci-lint config, GitHub Actions workflow.
3. `internal/k8s`: client from kubeconfig, discovery, one Table watch for
   Pods emitting row messages on a channel.
4. `internal/app`: root model with a table view and status bar.
5. Run against homelab. Tag `v0.0.1`.
