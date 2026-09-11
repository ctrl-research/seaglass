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

Status: M0 skeleton. Lists pods with live updates, server-side columns,
`j`/`k`/arrows to move, `q` to quit.
