# ADR 0002: Prometheus Operator and descheduler are checked, never bundled

Date: 2026-10-24 · Status: accepted

## Context

The score needs the Prometheus Operator's `PrometheusRule` CRD and a Prometheus that scrapes KubeVirt.
A descheduler is often present for containers. Both could be Helm subcharts.

## Decision

* **No subcharts.** The Prometheus Operator's CRDs are cluster-scoped; two charts owning them conflict,
  almost every KubeVirt cluster already runs a Prometheus, and platforms pin their own monitoring stack.
  A descheduler is the component this project replaces for VMs; bundling it would install the one thing
  that can fight the brain.
* **Three layers of checks instead:**
  1. chart render: `.Capabilities.APIVersions.Has` for `monitoring.coreos.com/v1/PrometheusRule` and
     `kubevirt.io/v1/VirtualMachineInstance`, `fail` with an actionable message; `values.schema.json`.
  2. install time (platform integration): the installer verifies the Prometheus Service and the KubeVirt CR
     before applying the release.
  3. runtime: conditions `PrometheusReachable`, `ScoreRuleLoaded` (vmi:drs_score has series),
     `KubeVirtAvailable`; `Ready=False` blocks every action; retries with backoff, never crash-loops.

## Consequences

* Users without the Prometheus Operator load `docs/examples/drs-rules.yaml` by hand (`prometheusRule.enabled=false`).
* The chart documents coexistence with a descheduler instead of configuring one.
