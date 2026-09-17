#!/usr/bin/env bash
# Install Flux into the seaglass-dev kind cluster and apply fixtures: a
# working Kustomization (podinfo) and a deliberately broken one. Idempotent.
# Used to exercise the Flux preset against real objects.
set -euo pipefail

CTX="${SEAGLASS_DEMO_CONTEXT:-kind-seaglass-dev}"
echo "== using context $CTX =="

if ! kubectl --context "$CTX" get ns flux-system >/dev/null 2>&1; then
  echo "== installing Flux =="
  flux --context "$CTX" install
fi

echo "== working source + Kustomization (podinfo) =="
flux --context "$CTX" create source git podinfo \
  --url=https://github.com/stefanprodan/podinfo --branch=master --interval=1m --export |
  kubectl --context "$CTX" apply -f -
flux --context "$CTX" create kustomization podinfo \
  --source=GitRepository/podinfo --path=./kustomize --prune=true \
  --interval=1m --target-namespace=default --export |
  kubectl --context "$CTX" apply -f -

echo "== deliberately broken Kustomization =="
flux --context "$CTX" create kustomization broken \
  --source=GitRepository/podinfo --path=./does-not-exist --prune=true \
  --interval=1m --export |
  kubectl --context "$CTX" apply -f -

echo "== done. Flux CRDs: =="
kubectl --context "$CTX" -n flux-system get kustomizations
echo
echo "Try: ./bin/seaglass --context $CTX -n flux-system"
echo "  :kustomizations   'broken' shows a not-ready badge"
echo "  J on podinfo  jumps to its GitRepository source"
echo "  R on a row    reconciles and waits for completion"
