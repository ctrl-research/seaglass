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
9. **Operators are configuration, not code.** Most operator support is
   three declarative things: an action that patches an annotation or field,
   a jump that follows a label or spec field to another object, and a badge
   that colors a row from a condition. seaglass ships those as a config
   layer with built-in presets, Flux first. k9s has no Flux features beyond
   community shell plugins; here a cert-manager or Argo CD preset is a YAML
   file and a fixture, not a milestone.

Non-goals for now: multi-cluster views, code plugins (Go or shell-arg
style; the M4 config layer is the plugin system), metrics/pulses
dashboards, Helm (outside Flux HelmReleases), xray-style trees, mouse
support, Windows.

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
- [x] "All namespaces" as a palette entry.
- [x] Row filter (`/`): case-insensitive substring terms (fuzzy is too loose
  over whole rows), live count, filter survives updates.
  Match highlighting deferred: Bubbles table cells are plain strings.
- [x] Detail view (`d`/`enter`): generic describe (metadata, scalar status,
  conditions, containers) that works for every type. YAML view (`y`) with
  heuristic highlighting, managedFields stripped, `c` copies via OSC52.
  Object views fetch once; `r` reloads. Live single-object watch later.
- [x] Column sort via a picker on `s` (natural order: ages, counts, text),
  `S` reverses, arrow in the header. `w` cycles auto/wide/narrow columns.
- [x] Header: six-line ASCII wordmark top left, six info fields beside it,
  a single horizontal rule beneath, no borders. Hidden below 22 rows or
  60 columns; fields drop before the logo when narrow; the help overlay
  reclaims the space.
- [x] Generated help overlay (`?`) from the keymap. All handlers match on
  `key.Binding`s in one keymap, so help cannot drift and M5 can override.
- [x] Persist last context/namespace/resource between runs (state.json in
  the state dir; flags override).

Gate: I use seaglass instead of k9s for all read-only browsing for a week.

### M2 — Operate (about 2 weeks) → `v0.0.3`

Build the patch/action engine first (see M4) and express the built-in
actions on it: rollout restart is an annotation patch, scale and suspend
are field patches, delete is a verb. Only exec, logs, port-forward, and
edit need bespoke code. This avoids writing the actions twice.

- [x] Action engine: match rule, key, merge patch from field paths or a
  delete verb, optional confirm, result as a status bar notice. Actions
  appear in the palette for the selected row and in help.
- [x] Rollout restart (`r`) as a patch action on Deployments,
  StatefulSets, DaemonSets.
- [x] Delete with confirm dialog (`ctrl+d`) and an `f` force toggle (grace 0).
  Every state-changing action confirms; config may opt out per action.
- [x] Scale for Deployments/StatefulSets/ReplicaSets via a generic prompt
  line (`=`, prefilled from the table's Ready/Desired cell) and `+`/`-`.
- [x] Logs (`l`): follow with pause on scroll, containers merged by server
  timestamp with a picker, since window, timestamps toggle, regex filter
  with highlight, wrap toggle, save to file. Previous-container logs later.
- [x] Exec shell into a container (`x`) over the API server with client-go
  remotecommand (websocket, SPDY fallback), rendered inside the TUI by an
  embedded terminal emulator so header and status bar stay visible;
  crumbs name the pod and container, the status bar the remote user.
  Resize forwarding, bash-or-sh, container picker, `ctrl+]` closes.
  A clean exit (shell exit or `ctrl+]`) returns to the table automatically
  with a notice; a real error (no shell in image) stays until esc.
- [x] Edit in `$EDITOR` (`e`, honoring `$KUBE_EDITOR`) from the table or
  object view: fetch, strip status/managedFields, open the editor, apply
  on save via update; a stale resourceVersion is reported as a conflict,
  schema/immutable errors as invalid, no change aborts.
- [x] Port-forward (`F`) a pod to a local port (auto-assigned): single
  declared port forwards directly, several open a picker, none prompt for
  one. A palette `port-forwards` panel lists active tunnels and cancels
  them; a status-bar `⇄N` indicator; forwards that die on their own are
  removed with an error. Torn down on context switch and quit.
- [x] Copy (`c`) the selected object's name, namespace, `namespace/name`,
  or a context- and namespace-scoped `kubectl get/describe/-o yaml`
  command to the clipboard via OSC52, chosen from a picker.
- [x] Rollout status (`R`) for Deployments, StatefulSets, DaemonSets: a
  view that polls and shows kubectl-style progress until complete or
  failed, with a spinner and elapsed time. A rollout restart opens it
  automatically.

Gate: k9s is uninstalled.

### M3 — Relationships and events (about 1 week) → `v0.0.4`

Owner references, label selectors, and field references are the three
jump kinds. Implement them as the jump engine (see M4) with the built-in
Kubernetes relationships as the default rule set, so operator presets
reuse them.

- Owner jump (`o`): Pod → ReplicaSet → Deployment, Job → CronJob, etc.
- Related jump (`shift+r`): Service → Endpoints → Pods, Deployment → Pods
  by selector, Pod → Node, Node → Pods, ConfigMap/Secret → mounting Pods.
- Events view per resource (`shift+e`) and a cluster-wide events stream
  sorted by last seen with warning highlighting.
- Pod status decoration: CrashLoopBackOff, OOMKilled, ImagePullBackOff,
  pending reasons surfaced in the row and detail view.

Gate: I can diagnose a failing rollout without leaving the tool.

### M4 — Operators via config, Flux preset (about 2 weeks) → `v0.0.5`

A declarative layer for operator support. Presets ship inside the binary
under `presets/*.yaml`; users override or add rules in `seaglass.yaml`.
Every rule is testable with a fixture object and runs through the same
patch, get, and watch paths as the built-in features.

Config types:

- **actions**: match on group/kind (globs allowed), a key or palette name,
  a patch (dotted paths to values, templated with `now`, `.metadata.*`,
  `.spec.*`), optional `confirm`, optional `wait` on a status field or
  condition with a timeout. Result reported in the status bar.
- **jumps**: from a label pair, an annotation, or a spec field shaped like
  a reference (`kind`/`name`/`namespace`) to a target kind. Shown in the
  detail view and under a related-objects key.
- **badges**: match plus a condition or field predicate, mapped to a row
  style (warning, error, muted) and an optional short tag.
- **commands**: the escape hatch. Run a program with the selected object's
  fields in the environment via `tea.ExecProcess`. Clearly labeled as the
  unsafe path; no argument templating beyond env vars.

Work items:

- [ ] Rule schema, loader, validation with line-numbered errors, and
  `seaglass presets list|show`.
- [ ] Action engine: JSON merge patch with dry-run, template rendering,
  confirm dialog, wait loop that watches one object until the predicate
  holds or times out.
- [ ] Jump engine over the three jump kinds; detail view lists jumps with
  the target's Ready state where the target has conditions.
- [ ] Badge engine applied in every table view.
- [ ] Commands via ExecProcess with terminal release and restore.
- [ ] Flux preset: reconcile (with source), suspend, resume; jumps for
  managed-by labels and `spec.sourceRef`; badges for Ready=False and
  `spec.suspend`; the `flux` resource group listing every Flux kind.
- [ ] Flux code items that config cannot express: parse
  `status.inventory.entries` (`ns_name_group_kind`) into a jumpable list;
  compose the trace chain "Managed by Kustomization/apps ← GitRepository/
  flux-system @ main/abc1234" by walking jump rules; show
  `lastAppliedRevision`, `lastAttemptedRevision`, `spec.dependsOn` with
  each dependency's Ready state at the top of Kustomization and
  HelmRelease detail.
- [ ] Generic resource-group view: one table merging several streams with
  a KIND column, not-ready first. Used by the `flux` group and a
  `workloads` group (Deployments, StatefulSets, DaemonSets, Jobs).
- [ ] Fixtures: `make demo-flux` runs `flux install` on the kind cluster and
  applies a working and a deliberately broken Kustomization. Unit tests
  run every preset rule against fixture objects.

Parked: `flux diff` (needs a local kustomize build), image automation
views, controller log correlation per object. cert-manager and Argo CD
presets are the first follow-ups and should each be a YAML file plus a
fixture.

Gate: I stop reaching for the `flux` CLI to check why something is not
Ready, and to reconcile it. Adding a suspend action for an in-house
operator takes five minutes and no Go.

### M5 — Config, polish, resilience (about 1 week) → `v0.0.6`

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

### M6 — Distribution (a few days) → `v0.1.0`

Only when it has earned it. Everything before this is `go install` only.

- GoReleaser: darwin/linux, arm64/amd64, checksums, GitHub Releases.
- Homebrew tap under the same GitHub org.
- README with a recording (vhs), quickstart, keymap table, config reference.
- Issue templates, CONTRIBUTING, license.

### Parked (after v0.1)

Multi-cluster fleet view, metrics-server integration for CPU/memory columns,
plugin/hook system, Helm releases view, RBAC "can I" view, node shell,
benchmarking, mouse support. cert-manager and Argo CD presets on the M4
config layer.

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
