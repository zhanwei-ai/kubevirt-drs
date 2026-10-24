#!/usr/bin/env bash
# Run `go` locally when available, otherwise inside the pinned golang container (podman or docker). The module
# and build caches live OUTSIDE the repository (${XDG_CACHE_HOME:-~/.cache}/kubevirt-drs): a cache inside the
# tree would be walked by controller-gen's paths=./... and by linters. FORCE_CONTAINER=1 skips a local go.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_IMAGE="${GO_IMAGE:-docker.io/library/golang:1.26-bookworm}"
if command -v go >/dev/null 2>&1 && [[ -z "${FORCE_CONTAINER:-}" ]]; then
  exec go "$@"
fi
engine="$(command -v podman || command -v docker || true)"
[[ -n "$engine" ]] || { echo "hack/go.sh: need go, podman or docker on PATH" >&2; exit 1; }
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/kubevirt-drs"
mkdir -p "$CACHE/gomod" "$CACHE/gocache" "$CACHE/gobin"
exec "$engine" run --rm -v "$ROOT:/src:Z" -v "$CACHE:/cache:Z" -w /src \
  -e GOMODCACHE=/cache/gomod -e GOCACHE=/cache/gocache -e GOBIN=/cache/gobin \
  -e GOFLAGS="${GOFLAGS:-}" -e CGO_ENABLED=0 -e GOTOOLCHAIN=auto \
  "$GO_IMAGE" go "$@"
