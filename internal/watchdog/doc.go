// Package watchdog cleans up after the one failure mode of pinning a target node: KubeVirt's
// addedNodeSelector can only narrow the VM's own constraints, so a target the planner mis-judged leaves the
// VMIM in Scheduling forever instead of picking another node. The watchdog deletes a brain-created VMIM
// (label drs.zhanwei.ai/recommendation present; evacuation and user migrations are never touched) that
// has been in Pending/Scheduling for longer than the grace period (2m), marks the recommendation Failed,
// and blacklists the (source, target) pair for one hour so the planner tries elsewhere.
//
// It also raises ExternalEvacuationsDetected on the policy when kubevirt-evacuation-* migrations exist for
// VMIs whose node is not cordoned (a descheduler is moving VMs; see docs/coexist-with-descheduler.md).
package watchdog
