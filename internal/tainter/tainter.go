// Package tainter is the optional soft tainter: it puts the PreferNoSchedule taint drs.zhanwei.ai/hot on
// nodes the brain considers hot, so kube-scheduler steers new VMs (and the fallback placement of any
// unpinned migration) away from them, and removes it when the node recovers. Same idea as the soft tainter
// OpenShift ships with its descheduler profile. PreferNoSchedule never evicts and never blocks.
package tainter

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

// Hot returns the nodes that should carry the taint: VMs on it score below the threshold floor, or VM
// demand (plus reserved moves) exceeds the target headroom.
func Hot(nodes []planner.Node, pol planner.Policy) map[string]bool {
	limit := pol.TargetHeadroom
	if limit <= 0 {
		limit = 0.8
	}
	hot := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.HasScore && n.Score < float64(pol.Level.MinScore) {
			hot[n.Name] = true
			continue
		}
		if n.AllocatableCPU > 0 && (n.VMDemandCPU+n.ReservedCPU)/n.AllocatableCPU > limit {
			hot[n.Name] = true
		}
	}
	return hot
}

// Reconcile adds the taint to hot nodes and removes it elsewhere; with enabled=false it removes it from
// every node. It returns the nodes that carry the taint afterwards and how many nodes it patched.
func Reconcile(ctx context.Context, c client.Client, nodes []corev1.Node, hot map[string]bool, enabled bool) (tainted []string, changed int, err error) {
	for i := range nodes {
		n := &nodes[i]
		has := hasTaint(n)
		want := enabled && hot[n.Name]
		if has == want {
			if has {
				tainted = append(tainted, n.Name)
			}
			continue
		}
		patch := client.MergeFrom(n.DeepCopy())
		if want {
			n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: drsv1alpha1.TaintHot, Value: "true", Effect: corev1.TaintEffectPreferNoSchedule})
		} else {
			kept := n.Spec.Taints[:0]
			for _, t := range n.Spec.Taints {
				if t.Key != drsv1alpha1.TaintHot {
					kept = append(kept, t)
				}
			}
			n.Spec.Taints = kept
		}
		if err := c.Patch(ctx, n, patch); err != nil {
			return tainted, changed, fmt.Errorf("patch taints of node %s: %w", n.Name, err)
		}
		changed++
		if want {
			tainted = append(tainted, n.Name)
		}
	}
	sort.Strings(tainted)
	return tainted, changed, nil
}

func hasTaint(n *corev1.Node) bool {
	for _, t := range n.Spec.Taints {
		if t.Key == drsv1alpha1.TaintHot {
			return true
		}
	}
	return false
}
