# ADR 0003: the brain creates migrations; it never evicts

Date: 2026-10-24 · Status: accepted

## Context

A descheduler moves a VM by evicting its virt-launcher pod. KubeVirt's webhook answers 429, marks the
VMI for evacuation, and an evacuation controller creates the migration; kube-scheduler picks the target.
Making that asynchronous handshake reliable required `EvictionsInBackground`, the
`descheduler.alpha.kubernetes.io/request-evict-only` and `eviction-in-progress` annotations and PDB
handling, and it still cannot choose the target node.

## Decision

The executor creates `VirtualMachineInstanceMigration` objects directly, with
`spec.addedNodeSelector: {kubernetes.io/hostname: <target>}` and the label
`drs.zhanwei.ai/recommendation: <name>`. It holds no verb on `pods/eviction` and no delete verb on pods
or VMs.

## Consequences

* The brain cannot restart a VM under any configuration: a migration of a non-migratable VMI fails and
  the VM keeps running; `evictionStrategy` is irrelevant to it.
* Target choice is ours, which is the point, but node fit must be checked by the planner;
  `addedNodeSelector` cannot bypass the VM's own constraints, so a wrong pick stalls in Scheduling. The
  watchdog (delete own VMIM after 2 min, blacklist the pair 1 h) and the soft tainter mitigate.
* Drain / maintenance mode keeps working through KubeVirt's `evictionStrategy: LiveMigrate`; the brain
  recognises those evacuations and stays out of their way.
