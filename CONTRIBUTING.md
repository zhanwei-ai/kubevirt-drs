# Contributing

Thanks for looking. The project is young; design discussions are as welcome as code.

## Ground rules

- **Sign your commits (DCO).** Every commit needs a `Signed-off-by: Name <email>` line (`git commit -s`), which
  certifies the [Developer Certificate of Origin](DCO). CI rejects unsigned commits.
- **Conventional commits**: `feat(planner): ...`, `fix(chart): ...`, `docs: ...`. No AI-attribution trailers.
- **One PR, one change.** Generated files (`zz_generated.deepcopy.go`, `config/crd/bases`, `charts/kubevirt-drs/crds`)
  are committed; run `make generate manifests` and commit the result, CI diffs them.
- **Decisions are written down** as ADRs in [`docs/adr`](docs/adr). A PR that changes a decision updates or
  supersedes the ADR in the same PR.

## Building without a local Go toolchain

`make` targets run in a `golang` container when `go` is not on `PATH` (see `hack/go.sh`). You need podman or docker.

```bash
make generate manifests   # controller-gen: deepcopy, CRDs, RBAC
make build test lint      # go build / go test / golangci-lint
make chart-lint rules-check   # helm lint + template, promtool check rules on the rendered PrometheusRule
```

## Testing levels

| Level | Where | What it proves |
| --- | --- | --- |
| unit | `internal/**/*_test.go` | planner decisions on table-driven scenarios (the VM1/VM2 "moved the wrong VM" case is a regression test) |
| envtest | `internal/**` | reconcilers against a real API server |
| e2e | `test/e2e/run.sh` (`make e2e`, CI workflow `e2e`) | kind with two workers + KubeVirt in software emulation + a scripted stand-in for Prometheus (`test/e2e/fakeprom`): Prometheus says a VM suffers → recommendation → pinned migration → the VM runs on the other node with the same UID → healthy again, nothing else moves. Wiring, not performance |
| lab | your cluster | calibration with stress-ng; see `docs/calibration.md` |

## Code of conduct

This project follows the [Contributor Covenant 2.1](https://www.contributor-covenant.org/version/2/1/code_of_conduct/).
