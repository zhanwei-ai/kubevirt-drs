# ADR 0004: the score rules use raw KubeVirt metrics only

Date: 2026-10-24 · Status: accepted

## Context

KubeVirt ships its own recording rules (`vmi:kubevirt_vmi_vcpu:count`, `vmi:kubevirt_vmi_swap_traffic_bytes:rate5m`,
`vmi:kubevirt_vmi_pgmajfaults:rate5m`, …) that the prototype reused. They exist only when the KubeVirt
CR sets `spec.monitorNamespace`, and their names have changed between releases.

## Decision

Every rule in `kubevirt-drs.score` reads raw `kubevirt_vmi_*` series exported by virt-handler and recomputes
the intermediates itself (`vmi:drs_vcpu:count` from `kubevirt_vmi_vcpu_seconds_total`, swap and page-fault
rates inline).

## Consequences

* One fewer silent breakage: the score survives a KubeVirt upgrade that renames its rules, and clusters
  that scrape KubeVirt without `monitorNamespace`.
* The raw metric names are still a contract with KubeVirt; the version matrix and the e2e run per
  KubeVirt minor guard it, and the runtime probe `kubevirt_vmi_vcpu_delay_seconds_total has series`
  catches a rename at once.
