# kubevirt-drs

**VMware DRS-style resource scheduling for KubeVirt: a per-VM score, and a brain that live-migrates the VM
that is actually suffering to the node where it will do better.**

> Status: **alpha**, v0.3.1. The score, the brain (`controller.enabled=true`; Manual by default,
> `FullyAutomated` available), the watchdog with its blacklist and the soft tainter are released, verified on a
> 3-node KubeVirt 1.9.1 lab cluster and by a kind e2e that runs on every PR. The levels are not calibrated on
> real load yet: read [docs/calibration.md](docs/calibration.md) before turning on `FullyAutomated`.
> Not affiliated with or endorsed by the KubeVirt project or Broadcom/VMware.

## Why

Kubernetes schedules a VM once, at start, by its *requests*. Nothing looks again. When a node gets
contended the VM slows down, kubelet does not care (CPU contention is not an eviction signal), and the
descheduler, if you run one, looks at node utilization, evicts *a* pod ordered by priority and QoS (every
virt-launcher ties), and lets kube-scheduler pick the target by requests again. VMware admins call the
missing piece DRS.

kubevirt-drs does the four DRS steps per VM:

| step | descheduler (stock) | OpenShift `KubeVirtRelieveAndMigrate` | kubevirt-drs |
| --- | --- | --- | --- |
| whether to move | node over a utilization threshold | node far from the cluster average (CPU, memory, PSI) | VM score low **and** host-caused **and** predicted gain > threshold |
| whom | priority / QoS order (random among VMs) | same, plus a cooldown filter | the lowest-scoring suffering VM |
| where | kube-scheduler, by requests | kube-scheduler, nudged by soft taints | the node with the best predicted score, pinned via `addedNodeSelector` |
| how | Eviction API, KubeVirt turns it into a migration | same, with `EvictionsInBackground` | creates the `VirtualMachineInstanceMigration` directly, no eviction |
| manual mode | dry-run logs | Predictive mode logs | `DRSRecommendation` objects, approve with one patch |
| runs on | any cluster | OpenShift only, needs `psi=1` | any KubeVirt + Prometheus Operator |

Because it never evicts, it can never restart a VM: the only objects it creates are migrations, and a
migration of a non-migratable VMI simply fails while the VM keeps running.

## The score

`vmi:drs_score` is 0..100 per running VMI, recomputed every minute from raw KubeVirt metrics (no guest
agent, no kernel parameters, nothing installed on nodes):

```
score = 100 × max(0, 1 − 4 × cpu_ready) × (1 − mem_pressure) × (1 − net_drop)
```

* `cpu_ready` = share of time the VM's vCPUs waited for a host core (`kubevirt_vmi_vcpu_delay_seconds_total`,
  libvirt's run delay = VMware's CPU ready time). 5 % ready scores 80, 25 % scores 0.
* `mem_pressure` = balloon reclaim + guest swap + major page faults, capped at 1.
* `net_drop` = dropped / total packets.

Efficiencies multiply, as in VMware's VM DRS Score. `node:drs_score:avg` and `cluster:drs_score:avg` are
vCPU-weighted. Details and the calibration method: [docs/scoring.md](docs/scoring.md),
[docs/calibration.md](docs/calibration.md).

## Install

The default install is score-only: the recording rules and the dashboard, nothing that moves a VM. The brain
is `controller.enabled=true` (next section). Requires a Prometheus Operator that scrapes KubeVirt (e.g.
kube-prometheus-stack with KubeVirt's ServiceMonitor; `kubevirt_vmi_vcpu_delay_seconds_total` must have series).

```bash
helm install kubevirt-drs oci://ghcr.io/zhanwei-ai/charts/kubevirt-drs --version 0.3.1 -n kubevirt-drs --create-namespace
# If your Prometheus Operator uses a ruleSelector, add its label:
#   --set prometheusRule.additionalLabels.release=kube-prometheus-stack
```

Then in Prometheus: `vmi:drs_score`. A Grafana dashboard ships as a ConfigMap (`dashboard.enabled`).
Without the operator, load [docs/examples/drs-rules.yaml](docs/examples/drs-rules.yaml) yourself.

## What the brain does

One cycle a minute, single replica, no webhooks:

1. **gate**: in-flight migrations (all of them, not only ours) under KubeVirt's limits; policy not paused;
   Prometheus, the rules and KubeVirt all healthy (`DRSPolicy` conditions).
2. **candidates**: running, live-migratable VMIs, not opted out, not in cooldown, score below the level's
   floor, and the low score is *host-caused* (CPU ready or balloon reclaim). A VM that is slow because of
   its own RAM is skipped: moving it would not help. This filter is the main difference to a node-utilization
   descheduler.
3. **target**: every other node that fits (requests, affinity, taints, CPU model, networks); predict the
   score after the move; pick the best.
4. **cost/benefit**: gain over the `migrationThreshold` level (Conservative 30 / Default 15 / Aggressive 5
   points), target must not end up worse than the source (no ping-pong), pair not blacklisted.
5. **record**: a `DRSRecommendation` with current score, expected score, reason and cost. `Manual` waits
   for `spec.approved: true`; `FullyAutomated` applies it: a VMIM pinned with `addedNodeSelector`.

Try it (Manual mode is the default; nothing moves until you approve):

```bash
helm upgrade --install kubevirt-drs oci://ghcr.io/zhanwei-ai/charts/kubevirt-drs -n kubevirt-drs \
  --set controller.enabled=true --set prometheus.url=http://kube-prometheus-stack-prometheus.monitoring.svc:9090
kubectl get drspolicy default                       # Ready must be True (see the conditions otherwise)
kubectl get drsrec -A                               # recommendations with current / expected score and the reason
kubectl -n <ns> patch drsrec <name> --type merge -p '{"spec":{"approved":true}}'   # apply one
kubectl -n <ns> get vmim -l drs.zhanwei.ai/recommendation=<name>                   # the migration it created
```

Opt a VM out with the annotation `drs.zhanwei.ai/automation: disabled` on `spec.template.metadata` of the
VirtualMachine (it propagates to the VMI); the descheduler's `prefer-no-eviction` works too.

Safety that does not depend on anything else being configured correctly: cooldown read from the VMI's own
`migrationState` (whoever migrated it), the descheduler's `prefer-no-eviction` annotation honoured, pending
recommendations re-validated every cycle and expired with a reason when they stop holding, proposed and
in-flight moves reserving capacity on their target and settling their source, a watchdog that only ever
deletes migrations it created (a pinned target that did not fit: deleted after 2 min, pair blacklisted 1 h),
and an `ExternalEvacuationsDetected` condition when something else is moving VMs. Optional soft tainter:
`spec.softTainter: true` puts a PreferNoSchedule taint on hot nodes so new VMs avoid them too. See
[docs/coexist-with-descheduler.md](docs/coexist-with-descheduler.md).

## Overhead

Steady state is close to nothing: one small pod (≈100 MiB at 100 VMs), 4 Prometheus series per VM plus 2
per node, a handful of instant queries a minute, zero per-node footprint. The real cost is the migrations it
decides on, bounded by the threshold, the cooldown, KubeVirt's parallel limits and your `MigrationPolicy`
bandwidth cap. Healthy cluster = zero migrations.

## Roadmap

| version | content |
| --- | --- |
| v0.1 | score rules, dashboard, chart (shipped) |
| v0.2 | controller in Manual mode: recommendations, approve-to-apply, events, metrics (shipped) |
| v0.3 | watchdog + blacklist, reserved capacity + settle period, soft tainter, rule-violation (priority-1) moves, kind e2e in CI: FullyAutomated move, Manual approval, watchdog across a restart (shipped, 0.3.1) |
| next | calibrated levels and benchmark vs descheduler (lab work, see [docs/calibration.md](docs/calibration.md)) |
| v1.0 | version matrix (KubeVirt 1.9+, k8s 1.34+), docs |
| later | optional PSI node signal, Prometheus-free mode (scrape virt-handler directly), Headlamp panel, kubectl plugin |

Not planned: DPM (power management), predictive DRS, load-aware *initial* placement (soft taints help,
a scheduler plugin would be the real thing).

## Development

Every `make` target works without a local Go toolchain: `hack/go.sh` runs `go` in a container when it is not on
`PATH` (podman or docker). Details and the testing levels: [CONTRIBUTING.md](CONTRIBUTING.md).

```bash
make build test lint          # go build / go test (writes coverage.out) / golangci-lint
make generate manifests       # controller-gen: deepcopy, CRDs, RBAC; commit the result, CI diffs it
make chart-lint rules-check   # helm lint + template + kubeconform; promtool on the exported rules
make e2e                      # kind + KubeVirt (software emulation) + a scripted Prometheus, about 12 min
make clean                    # bin/, dist/, coverage files, packaged charts
```

`make e2e` needs kind, kubectl, helm and docker. It is the script CI runs ([test/e2e/run.sh](test/e2e/run.sh));
CI pins kind v0.33.0, `kindest/node:v1.36.4` and KubeVirt v1.9.1 in
[.github/workflows/e2e.yml](.github/workflows/e2e.yml), so set `KUBEVIRT_VERSION` and `KIND_NODE_IMAGE` the
same way to reproduce a run. The Makefile picks podman over docker when both are installed; kind then needs
`KIND_EXPERIMENTAL_PROVIDER=podman`, or pass `ENGINE=docker make e2e`. `KEEP=1` keeps the cluster
`kubevirt-drs-e2e` for a look afterwards; delete it before the next run, the scenarios expect a fresh cluster.

## Docs

[concepts](docs/concepts.md) · [scoring](docs/scoring.md) · [calibration](docs/calibration.md) ·
[coexisting with a descheduler](docs/coexist-with-descheduler.md) · [limits](docs/limits.md) ·
[decisions (ADRs)](docs/adr) · [changelog](CHANGELOG.md) · [contributing](CONTRIBUTING.md) · [security](SECURITY.md)

Apache-2.0.
