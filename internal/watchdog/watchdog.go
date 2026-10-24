// Package watchdog cleans up after the one failure mode of pinning a target node: KubeVirt's
// addedNodeSelector can only narrow the VM's own constraints, so a target the planner mis-judged leaves the
// migration in Pending/Scheduling forever instead of picking another node. The watchdog deletes a
// brain-created migration (label drs.zhanwei.ai/recommendation present; evacuations and user migrations are
// never touched) that has been waiting longer than the grace period, marks its recommendation Failed and
// blacklists the (source, target) pair so the planner tries elsewhere.
package watchdog

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/executor"
	"github.com/zhanwei-ai/kubevirt-drs/internal/kube"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

// Watchdog is run once per cycle with the migrations the cycle already listed.
type Watchdog struct {
	Client    client.Client
	Exec      *executor.Executor
	Blacklist func(planner.Pair, time.Time)
	Log       logr.Logger
}

// Stalled reports whether a move is a brain-created migration still waiting for a node after `grace`.
func Stalled(mv kube.Move, now time.Time, grace time.Duration) bool {
	if mv.Recommendation == "" || !mv.InFlight {
		return false
	}
	switch mv.Phase {
	case kubevirtv1.MigrationPhaseUnset, kubevirtv1.MigrationPending, kubevirtv1.MigrationScheduling:
	default:
		return false
	}
	return !mv.Created.IsZero() && now.Sub(mv.Created) >= grace
}

// Run deletes stalled migrations, fails their recommendations and blacklists the pairs. It returns how many
// it cleaned up.
func (w *Watchdog) Run(ctx context.Context, moves []kube.Move, now time.Time, grace, blacklistFor time.Duration) (int, error) {
	n := 0
	for _, mv := range moves {
		if !Stalled(mv, now, grace) {
			continue
		}
		mig := &kubevirtv1.VirtualMachineInstanceMigration{ObjectMeta: metav1.ObjectMeta{Name: mv.Name, Namespace: mv.VMI.Namespace}}
		if err := w.Client.Delete(ctx, mig); err != nil && !apierrors.IsNotFound(err) {
			return n, fmt.Errorf("delete stalled migration %s/%s: %w", mv.VMI.Namespace, mv.Name, err)
		}
		msg := fmt.Sprintf("migration %s stayed in %s for %s: target %s did not fit after all (a predicate the planner does not model, e.g. inter-pod affinity or a network attachment). Migration deleted, pair %s->%s blacklisted for %s",
			mv.Name, phaseName(mv.Phase), now.Sub(mv.Created).Round(time.Second), mv.Target, mv.Source, mv.Target, blacklistFor)
		var rec drsv1alpha1.DRSRecommendation
		if err := w.Client.Get(ctx, types.NamespacedName{Namespace: mv.VMI.Namespace, Name: mv.Recommendation}, &rec); err == nil {
			if err := w.Exec.Fail(ctx, &rec, msg); err != nil {
				return n, err
			}
			w.Exec.Event(&rec, corev1.EventTypeWarning, "DRSMigrationStalled", "Cleanup", msg)
		} else if !apierrors.IsNotFound(err) {
			return n, err
		}
		if w.Blacklist != nil && mv.Source != "" && mv.Target != "" {
			w.Blacklist(planner.Pair{Source: mv.Source, Target: mv.Target}, now.Add(blacklistFor))
		}
		w.Log.Info("stalled migration cleaned up", "migration", mv.Name, "vmi", mv.VMI.String(), "source", mv.Source, "target", mv.Target, "phase", phaseName(mv.Phase))
		n++
	}
	return n, nil
}

func phaseName(p kubevirtv1.VirtualMachineInstanceMigrationPhase) string {
	if p == "" {
		return "Unset"
	}
	return string(p)
}
