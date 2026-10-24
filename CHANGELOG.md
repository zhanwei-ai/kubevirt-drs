# Changelog

All notable changes to this project are documented here. The format follows Keep a Changelog; versions follow SemVer.

## [Unreleased]

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

### Not yet
- Watchdog (stalled pinned migrations), blacklist, soft tainter, calibrated levels: v0.3.0.

## [0.1.0] - 2026-10-24

Score-only release: installs the DRS Score, moves nothing. The controller arrives in 0.2.0.

### Added
- Project scaffold: Go module, `drs.zhanwei.ai/v1alpha1` API types (`DRSPolicy`, `DRSRecommendation`), manager entry point.
- Helm chart `kubevirt-drs` with the DRS Score recording rules (`vmi:drs_score` and components, node and cluster aggregates), optional descheduler bridge rule, Grafana dashboard, and controller templates gated behind `controller.enabled`.
- Design docs and ADRs.
- Release pipeline: image and chart on GHCR, cosign keyless signatures, CycloneDX SBOM attestation, GitHub release with a post-publish verification job.

[Unreleased]: https://github.com/zhanwei-ai/kubevirt-drs/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/zhanwei-ai/kubevirt-drs/releases/tag/v0.1.0
