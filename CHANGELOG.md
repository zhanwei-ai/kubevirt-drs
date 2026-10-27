# Changelog

All notable changes to this project are documented here. The format follows Keep a Changelog; versions follow SemVer.

## [Unreleased]

### Added
- kind e2e: two more scenarios after the FullyAutomated one: Manual mode (the recommendation waits unapproved
  for two cycles, then a hand approval turns it into the pinned migration) and the watchdog (a recommendation
  pinned to a node that does not exist is cleaned up after the grace period, Failed, blacklisted with a
  Warning Event, and the blacklist survives a controller restart).

### Fixed
- A pending recommendation no longer expires itself: its own reservation on the target was counted on top of
  the VM when the move was re-validated, so a VM using more than 40 % of the target's cores was judged "would
  gain only 0 points" one cycle after it was recommended, and the brain alternated between two squeezed VMs
  forever (lab cluster, 2026-10-27). The kind e2e now gives vm2 45 % of a node to catch this.
- The blacklist survives a controller restart: it is reloaded from `status.blacklistedPairs` on the first cycle.

### Changed
- Cost estimate: the migration time now divides guest memory by the slower of the configured
  `bandwidthPerMigration` and the median transfer rate recent migrations actually achieved
  (`kubevirt_vmi_migration_memory_transfer_rate_bytes`, 24h window).

## [0.3.0] - 2026-10-25

The safety layer for automatic mode, verified on the lab cluster: a stuck pinned migration cleaned up in 2m44s, two squeezed VMs sent to two different targets, the hot taint on and off with the load.

### Added
- **Watchdog**: a brain-created migration still Pending/Scheduling after `spec.watchdog.gracePeriod` (2m) is
  deleted, its recommendation marked Failed with the reason, and the (source, target) pair blacklisted for
  `spec.watchdog.blacklistFor` (1h). Evacuations and user migrations are never touched. Status shows the
  blacklisted pairs; metric `kubevirt_drs_stalled_migrations_total`.
- **Reserved capacity and settle period**: proposed (pending) and in-flight moves reserve their VM's CPU
  demand on the target before the metrics show it, and a node a move just left is not a source again until
  the smoothing window passes. Two VMs are no longer sent to the same target in consecutive cycles.
- **Soft tainter** (`spec.softTainter: true`): PreferNoSchedule taint `drs.zhanwei.ai/hot` on nodes whose VMs
  score below the threshold floor or whose VM demand exceeds the headroom; removed on recovery. Status lists
  the hot nodes; metric `kubevirt_drs_hot_nodes`.
- A pinned migration that KubeVirt's own pending-target timeout fails before it started blacklists its pair as well.

### Fixed
- `helm upgrade --reuse-values` from a 0.2 release no longer fails on the missing `policy.watchdog` values.

### Known
- The blacklist is kept in memory: a controller restart forgets it (the policy status still lists it). Fixed in the next release.

## [0.2.0] - 2026-10-24

The brain, verified end to end on a 3-node KubeVirt 1.9.1 cluster in Manual mode (see the fixes below).

### Added
- The brain, Manual mode (`controller.enabled=true`): a once-a-minute cycle reads the recording rules, builds
  the planner's view (running VMIs, their virt-launcher pods, nodes, every in-flight migration, the KubeVirt
  CR's parallel limits and bandwidth), and records moves as `DRSRecommendation`s. `FullyAutomated` applies them
  itself; `Manual` waits for `spec.approved: true`, which the recommendation reconciler turns into a
  `VirtualMachineInstanceMigration` pinned with `addedNodeSelector` at once.
- Planner: candidates are low-scoring **and** host-caused (CPU ready / balloon reclaim); fit = taints,
  nodeSelector / required node affinity, CPU + memory requests (from kube-state-metrics); target = best predicted
  score; cost/benefit per `migrationThreshold`; no-ping-pong guard; one move per source node per cycle;
  priority-1 moves when a VM violates a rule where it runs. Three-layer cooldown read from the VMI's own
  migration history, whoever migrated it.
- `DRSPolicy` status: conditions `PrometheusReachable`, `ScoreRuleLoaded`, `KubeVirtAvailable`, `Ready`,
  `ExternalEvacuationsDetected`; cluster score, pending recommendations, in-flight migrations.
- Metrics `kubevirt_drs_*` (ready, cycle duration, cycles, recommendations, migrations, cluster score, in-flight).
- VMI annotation `drs.zhanwei.ai/score` (5-point buckets, `scoreAnnotation: false` to disable).

### Fixed (found on the first real run)
- A cordoned node is not a rule violation: node-lifecycle taints no longer trigger priority-1 moves (they still exclude targets).
- A VM with a migration in flight is never recommended again.
- Pending recommendations are re-validated every cycle and expire with a reason when the VM moved, recovered, or the target no longer helps.
- Events were rejected: RBAC for `events.k8s.io` added.
- Collected metrics are keyed by (VMI, node) so the old node's lingering series after a migration cannot win.

### Not yet
- Watchdog (stalled pinned migrations), blacklist, soft tainter, settle period for proposed moves, calibrated levels: v0.3.0.

## [0.1.0] - 2026-10-24

Score-only release: installs the DRS Score, moves nothing. The controller arrives in 0.2.0.

### Added
- Project scaffold: Go module, `drs.zhanwei.ai/v1alpha1` API types (`DRSPolicy`, `DRSRecommendation`), manager entry point.
- Helm chart `kubevirt-drs` with the DRS Score recording rules (`vmi:drs_score` and components, node and cluster aggregates), optional descheduler bridge rule, Grafana dashboard, and controller templates gated behind `controller.enabled`.
- Design docs and ADRs.
- Release pipeline: image and chart on GHCR, cosign keyless signatures, CycloneDX SBOM attestation, GitHub release with a post-publish verification job.

[Unreleased]: https://github.com/zhanwei-ai/kubevirt-drs/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/zhanwei-ai/kubevirt-drs/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/zhanwei-ai/kubevirt-drs/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/zhanwei-ai/kubevirt-drs/releases/tag/v0.1.0
