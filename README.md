# seaglass

A Kubernetes TUI. Single cluster, live by default, one code path for every
resource. See [PLAN.md](PLAN.md) for the roadmap.

## Install

Homebrew:

```sh
brew install ctrl-research/tap/seaglass
```

Or the install script (downloads the latest release for your OS/arch to
`~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/ctrl-research/seaglass/main/install.sh | sh
```

Pin a version with `SEAGLASS_VERSION=vX.Y.Z` or change the target with
`SEAGLASS_BIN_DIR`. From source: `go install github.com/ctrl-research/seaglass/cmd/seaglass@latest`.

## Configuration

```sh
seaglass config init       # write a commented ~/.config/seaglass/seaglass.yaml
seaglass config validate   # check it (and the built-in presets); reports file:line
seaglass presets list      # what ships built in (flux, core)
```

## Run


```sh
make run                                   # current kube context and namespace
make run ARGS="--context admin@homelab -n kube-system"
```

## Develop

```sh
make test                                       # unit tests
kind create cluster --name seaglass-dev         # throwaway cluster
make test-integration CONTEXT=kind-seaglass-dev # list + watch against it
make run ARGS="--context kind-seaglass-dev -A --debug"
```

Debug logs go to `~/.local/state/seaglass/seaglass.log` (or `$XDG_STATE_HOME`).

seaglass remembers the last context, and per context the last namespace and
resource, in `state.json` next to the log. Flags override what it remembers.

## Keys

| Key | Action |
|---|---|
| `:` or `ctrl+p` | Command palette: fuzzy search resource types, namespaces, contexts |
| `enter` | Open the selected palette item |
| `/` | Filter rows: every word must appear somewhere in the row; `enter` keeps it, `esc` clears |
| `s` | Sort by a column (pick from the palette); choose the same column again or press `S` to reverse |
| `w` | Cycle columns: auto (fit to width), wide (all), narrow (essentials) |
| `enter` or `d` | Detail view of the selected object: metadata, status, conditions, containers |
| `x` | Shell into the selected pod inside the TUI (bash if present, else sh); picks a container when there are several. Keys go to the shell; `ctrl+]` or the shell's own `exit` closes it and returns to the table |
| `F` | Port-forward the selected pod to a local port; picks or prompts for the remote port. The palette's `port-forwards` panel lists active tunnels and cancels them with `ctrl+d` (after a confirm) |
| `l` | Logs of the selected pod, all containers merged by time, following |
| `E` | Events for the selected object, filtered by involved object; warnings highlighted |
| `o` | Jump to the owner of the selected object (Pod → ReplicaSet → Deployment, Job → CronJob); repeat to walk up |
| `J` | Jump to related resources: Deployment/Service → its pods by selector, Pod → its node, Node → its pods |
| In logs: `c` container, `s` since window, `t` timestamps, `w` wrap, `f` follow, `/` regex filter, `S` save to file |
| `y` | YAML of the selected object, managed fields stripped |
| `e` | Edit the selected object in `$EDITOR` (`$KUBE_EDITOR` wins); save to apply, no change aborts |
| `c` | Copy the selected object's name, `namespace/name`, or a ready-to-run `kubectl` command to the clipboard |
| In detail/YAML: `y`/`d` switch, `c` copy YAML, `r` reload, `g`/`G` top/bottom |
| `esc` | Clear the filter, close the palette, or go back one view |
| `j`/`k`, arrows, `pgup`/`pgdn`, `g`/`G` | Move in the table |
| `r` | Rollout restart the selected Deployment, StatefulSet, or DaemonSet, after a confirm |
| `R` | Follow the rollout status of the selected Deployment, StatefulSet, or DaemonSet until it completes; a restart opens this automatically |
| `=` | Scale the selected Deployment, StatefulSet, or ReplicaSet: prompts for replicas, prefilled from the table |
| `+` / `-` | Scale by one replica up or down |
| `ctrl+d` | Delete the selected object; `f` in the dialog toggles force (grace period 0) |

Every action that changes the cluster (restart, scale, delete) asks for
confirmation first: `y` runs it, `n` or `esc` cancels.
| `?` | Help overlay listing every key for the current view |
| `q` or `ctrl+c` | Quit |

Pod and container statuses are colored by severity: healthy states green,
transient ones amber, failures like CrashLoopBackOff, OOMKilled, and
ImagePullBackOff red. An object's detail view shows per-container state,
restart counts, and problem reasons.

Contextual actions (delete, restart, scale, reconcile) and config
`commands` are hidden from the palette by default so a filter can't select one by accident; press
`ctrl+a` in the palette to show them, or use their direct keys. 

Palette tips: `deploy` finds deployments, `ns kube` narrows to namespaces,
`ns all` returns to all namespaces. Resource short names work (`ks` →
kustomizations, `po` → pods) via the API server's own shortNames, and an
exact short name always ranks first. Contexts are never mixed into a plain
filter: the palette's `clusters` entry opens a table of all contexts, and
`ctx <name>` switches directly; either way a switch asks for confirmation. The
palette also has `flux` and `workloads` group views (one table across
several kinds, not-ready first), a `clusters` context table, `events`,
`quit`, and `help` actions, and lists
the actions available for the selected row at the top. Choosing a resource pushes a new view;
choosing a namespace keeps the current resource and resets the view stack;
choosing a context reconnects.

Status: M1 feature-complete, in the dogfood week. Any list+watch resource
type, live updates, server-side columns, palette, row filter, sort, detail
and YAML views, header, remembered position, help.

## Driving the TUI headlessly

`hack/drive.exp` runs seaglass in a pseudo-terminal, sends keys one second
apart, and records the raw output. Useful for smoke tests against a kind
cluster:

```sh
make build
hack/drive.exp 120 30 /tmp/out.raw --context kind-seaglass-dev -A -- ":" "deploy" "\\r" "\\033" "q"
```

## Operator presets (M4, in progress)

seaglass ships declarative operator rules (actions, jumps, badges, commands,
and resource groups) as embedded presets, starting with Flux (and a small
core preset for the workloads group). Users add or override rules in
`$XDG_CONFIG_HOME/seaglass/seaglass.yaml`; `disablePresets: [flux]` turns one
off. Inspect them with:

```sh
seaglass presets list
seaglass presets show flux
seaglass presets validate   # checks presets + your seaglass.yaml
```

Actions run through the same patch/wait paths as the built-ins: an action
rule fetches the object, renders its patch template (with `{{now}}`,
`{{.input}}`, and `{{.spec...}}` paths), applies the merge patch, and waits
for its predicate. Jumps add to the `J` related-jump picker: a field
reference (`spec.sourceRef`), a label pair (managed-by), or a label
selector. Config actions appear on their key and in the palette. Badges
color a row and add a `(tag)` when a condition or field predicate holds;
the table fetches full objects only for resource types a badge targets.
