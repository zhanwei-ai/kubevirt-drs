# Changelog

All notable changes to this project are documented here. The format follows Keep a Changelog; versions follow SemVer.

## [Unreleased]

## [0.1.0] - 2026-10-24

Score-only release: installs the DRS Score, moves nothing. The controller arrives in 0.2.0.

### Added
- Project scaffold: Go module, `drs.zhanwei.ai/v1alpha1` API types (`DRSPolicy`, `DRSRecommendation`), manager entry point.
- Helm chart `kubevirt-drs` with the DRS Score recording rules (`vmi:drs_score` and components, node and cluster aggregates), optional descheduler bridge rule, Grafana dashboard, and controller templates gated behind `controller.enabled`.
- Design docs and ADRs.
- Release pipeline: image and chart on GHCR, cosign keyless signatures, CycloneDX SBOM attestation, GitHub release with a post-publish verification job.

[Unreleased]: https://github.com/zhanwei-ai/kubevirt-drs/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/zhanwei-ai/kubevirt-drs/releases/tag/v0.1.0
