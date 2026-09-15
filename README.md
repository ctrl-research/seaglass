# seaglass

A Kubernetes TUI. Single cluster, live by default, one code path for every
resource. See [PLAN.md](PLAN.md) for the roadmap.

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
| `l` | Logs of the selected pod, all containers merged by time, following |
| In logs: `c` container, `s` since window, `t` timestamps, `w` wrap, `f` follow, `/` regex filter, `S` save to file |
| `y` | YAML of the selected object, managed fields stripped |
| `e` | Edit the selected object in `$EDITOR` (`$KUBE_EDITOR` wins); save to apply, no change aborts |
| In detail/YAML: `y`/`d` switch, `c` copy YAML, `r` reload, `g`/`G` top/bottom |
| `esc` | Clear the filter, close the palette, or go back one view |
| `j`/`k`, arrows, `pgup`/`pgdn`, `g`/`G` | Move in the table |
| `r` | Rollout restart the selected Deployment, StatefulSet, or DaemonSet, after a confirm |
| `=` | Scale the selected Deployment, StatefulSet, or ReplicaSet: prompts for replicas, prefilled from the table |
| `+` / `-` | Scale by one replica up or down |
| `ctrl+d` | Delete the selected object; `f` in the dialog toggles force (grace period 0) |

Every action that changes the cluster (restart, scale, delete) asks for
confirmation first: `y` runs it, `n` or `esc` cancels.
| `?` | Help overlay listing every key for the current view |
| `q` or `ctrl+c` | Quit |

Palette tips: `deploy` finds deployments, `ns kube` narrows to namespaces,
`ns all` returns to all namespaces, `ctx prod` narrows to contexts. The
palette also has `quit` (also `exit` or `:q`) and `help` actions, and lists
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
