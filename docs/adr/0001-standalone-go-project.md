# ADR 0001: a standalone Go project, not a platform-specific add-on

Date: 2026-10-24 · Status: accepted

## Context

The design (per-VM score as Prometheus recording rules, a once-a-minute brain that picks the VM and the
target node) was prototyped for one Kubernetes distribution's installer, in Python, because that installer
already shipped a Python toolbox and air-gap pipeline. The prototype's own write-up listed three triggers
for switching to Go: customer image scanning, a Manual mode that must react to approvals at once, and
"deciding to open-source it".

## Decision

* Own repository, Apache-2.0, API group `drs.zhanwei.ai`.
* Go with controller-runtime: typed KubeVirt objects, a distroless image with no OS packages, watch-based
  approval instead of polling, and the language every neighbour (KubeVirt, descheduler, kube-scheduler) uses.
* Any platform consumes it as a released chart + image; none builds it from source or vendors it.

## Consequences

* About one extra day compared with the Python prototype; the PrometheusRule, CRDs and VMIM usage are
  language-independent and carried over unchanged.
* The project must stand on its own: docs, version matrix, e2e on kind, release signing and SBOM.
