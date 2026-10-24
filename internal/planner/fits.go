package planner

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	schedhelper "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
)

// FitResult says whether a VM's launcher pod could run on a node and, if not, the first reason why.
type FitResult struct {
	OK     bool
	Reason string
}

func fit(reason string) FitResult { return FitResult{OK: reason == "", Reason: reason} }

// lifecycleTaints are set by Kubernetes itself to describe a node's state (cordoned, pressure, not ready).
// They are not rules a VM can violate: a cordon is an admin decision (and a drain already evacuates), the
// others are node_ha / kubelet territory. They still exclude a node as a migration TARGET.
var lifecycleTaints = map[string]bool{
	corev1.TaintNodeUnschedulable:      true,
	corev1.TaintNodeNotReady:           true,
	corev1.TaintNodeUnreachable:        true,
	corev1.TaintNodeMemoryPressure:     true,
	corev1.TaintNodeDiskPressure:       true,
	corev1.TaintNodePIDPressure:        true,
	corev1.TaintNodeNetworkUnavailable: true,
}

// ViolatesRules reports whether the VM breaks a placement rule on the node it runs on: an untolerated
// NoSchedule / NoExecute taint that an admin put there, or a nodeSelector / required node affinity that no
// longer matches (labels changed). A true result is VMware's "mandatory" (priority 1) recommendation.
// Cordon, NotReady and pressure taints are deliberately NOT rules (see lifecycleTaints).
func ViolatesRules(vm *VM, node *Node) FitResult {
	if node == nil || node.Obj == nil || vm.Pod == nil {
		return fit("")
	}
	pod := vm.Pod
	if taint, untolerated := schedhelper.FindMatchingUntoleratedTaint(klog.Background(), node.Obj.Spec.Taints, pod.Spec.Tolerations, func(t *corev1.Taint) bool {
		return (t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute) && !lifecycleTaints[t.Key]
	}, false); untolerated {
		return fit("taint " + taint.Key + "=" + taint.Value + ":" + string(taint.Effect) + " not tolerated")
	}
	match, err := nodeaffinity.GetRequiredNodeAffinity(pod).Match(node.Obj)
	if err != nil {
		return fit("node affinity: " + err.Error())
	}
	if !match {
		return fit("nodeSelector / required node affinity not satisfied")
	}
	return fit("")
}

// Fits evaluates the scheduling predicates the migration's TARGET pod will face on `node`, using the source
// virt-launcher pod as the template (KubeVirt builds the target pod from the same VMI spec, so nodeSelector,
// affinity, tolerations and requests are identical; addedNodeSelector only narrows them). Checked:
//
//   - node Ready and not cordoned
//   - no NoSchedule / NoExecute taint the pod does not tolerate
//   - pod.spec.nodeSelector + requiredDuringScheduling node affinity (this is where KubeVirt puts the CPU
//     model / feature labels)
//   - with checkResources: node allocatable minus requested >= the pod's CPU / memory requests, when the
//     node's requested totals are known
//
// Not checked (left to the scheduler; a miss ends in the watchdog): inter-pod (anti-)affinity, topology
// spread, extended resources, volume topology, per-node network attachment availability.
func Fits(vm *VM, node *Node, checkResources bool) FitResult {
	if node == nil || node.Obj == nil {
		return fit("node unknown")
	}
	if !node.Ready {
		return fit("node not ready or cordoned")
	}
	if vm.Pod == nil {
		return fit("")
	}
	pod := vm.Pod
	// enableComparisonOperators=false: the scheduler's TaintTolerationComparisonOperators gate is alpha; the
	// conservative reading matches what a default kube-scheduler does.
	if taint, untolerated := schedhelper.FindMatchingUntoleratedTaint(klog.Background(), node.Obj.Spec.Taints, pod.Spec.Tolerations, func(t *corev1.Taint) bool {
		return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
	}, false); untolerated {
		return fit("taint " + taint.Key + "=" + taint.Value + ":" + string(taint.Effect) + " not tolerated")
	}
	match, err := nodeaffinity.GetRequiredNodeAffinity(pod).Match(node.Obj)
	if err != nil {
		return fit("node affinity: " + err.Error())
	}
	if !match {
		return fit("nodeSelector / required node affinity not satisfied")
	}
	if checkResources {
		cpu, mem := PodRequests(pod)
		if node.RequestedCPU >= 0 && node.AllocatableCPU-node.RequestedCPU < cpu {
			return fit("insufficient cpu")
		}
		if node.RequestedMemoryBytes >= 0 && node.AllocatableMemoryBytes-node.RequestedMemoryBytes < mem {
			return fit("insufficient memory")
		}
	}
	return fit("")
}

// PodRequests sums the containers' CPU (cores) and memory (bytes) requests; init containers are ignored
// (virt-launcher's are tiny and finished).
func PodRequests(pod *corev1.Pod) (cpuCores float64, memBytes int64) {
	for i := range pod.Spec.Containers {
		r := pod.Spec.Containers[i].Resources.Requests
		if q, ok := r[corev1.ResourceCPU]; ok {
			cpuCores += quantityCores(q)
		}
		if q, ok := r[corev1.ResourceMemory]; ok {
			memBytes += q.Value()
		}
	}
	return
}

func quantityCores(q resource.Quantity) float64 { return float64(q.MilliValue()) / 1000 }
