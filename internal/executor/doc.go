// Package executor turns a planner decision into API objects and nothing else:
//
//   - creates the DRSRecommendation (approved = policy is FullyAutomated);
//   - on an approved Pending recommendation creates the VirtualMachineInstanceMigration with
//     spec.addedNodeSelector {kubernetes.io/hostname: target} and the label drs.zhanwei.ai/recommendation,
//     stamps the VMI annotations (last-migrated-at, last-reason) and emits one Event;
//   - expires unapproved recommendations past their TTL;
//   - writes the score annotation when a VM crosses a 5-point bucket (policy.scoreAnnotation).
//
// It never uses the Eviction API and never deletes a pod or a VM: the only destructive verb it holds is
// delete on VMIMs, and the watchdog is the only caller.
package executor
