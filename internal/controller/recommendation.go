package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/executor"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

// RecommendationReconciler applies a recommendation the moment spec.approved turns true (Manual mode's
// "Apply" button) and copies the migration's outcome back onto it.
type RecommendationReconciler struct {
	Client client.Client
	Exec   *executor.Executor
	Log    logr.Logger
	// Blacklist is the cycle's blacklist; a pinned migration KubeVirt itself fails before it ever started
	// (its target pod never scheduled, KubeVirt's own pending timeout fired first) bans the pair the same
	// way the watchdog does. BlacklistFor is the policy's duration.
	Blacklist    func(planner.Pair, time.Time)
	BlacklistFor func() time.Duration
}

func (r *RecommendationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("drsrecommendation").
		For(&drsv1alpha1.DRSRecommendation{}).
		Watches(&kubevirtv1.VirtualMachineInstanceMigration{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			rec := obj.GetLabels()[drsv1alpha1.LabelRecommendation]
			if rec == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: rec}}}
		})).
		Complete(r)
}

func (r *RecommendationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rec drsv1alpha1.DRSRecommendation
	if err := r.Client.Get(ctx, req.NamespacedName, &rec); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := r.Log.WithValues("recommendation", req.String(), "phase", rec.Status.Phase)
	switch rec.Status.Phase {
	case drsv1alpha1.PhasePending, "":
		if !rec.Spec.Approved {
			return ctrl.Result{}, nil
		}
		now := time.Now()
		if err := r.Exec.Apply(ctx, &rec, now); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("applied", "migration", rec.Status.Migration, "result", rec.Status.Phase)
		metricMigrations.WithLabelValues(string(rec.Status.Phase)).Inc()
		return ctrl.Result{}, nil
	case drsv1alpha1.PhaseApplied:
		if rec.Status.Migration == "" {
			return ctrl.Result{}, nil
		}
		var mig kubevirtv1.VirtualMachineInstanceMigration
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: rec.Namespace, Name: rec.Status.Migration}, &mig); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil // garbage-collected by KubeVirt; nothing to copy
			}
			return ctrl.Result{}, err
		}
		if mig.Status.Phase == kubevirtv1.MigrationFailed && r.Blacklist != nil && neverStarted(&mig) {
			d := time.Hour
			if r.BlacklistFor != nil {
				d = r.BlacklistFor()
			}
			r.Blacklist(planner.Pair{Source: rec.Spec.SourceNode, Target: rec.Spec.TargetNode}, time.Now().Add(d))
			log.Info("pinned migration failed before it started: pair blacklisted", "source", rec.Spec.SourceNode, "target", rec.Spec.TargetNode, "for", d)
		}
		return ctrl.Result{}, r.Exec.SyncOutcome(ctx, &rec, &mig)
	}
	return ctrl.Result{}, nil
}

// neverStarted: the migration failed without ever transferring (no start timestamp): the target pod did not
// schedule, so the target node is the problem, not the VM.
func neverStarted(mig *kubevirtv1.VirtualMachineInstanceMigration) bool {
	ms := mig.Status.MigrationState
	return ms == nil || ms.StartTimestamp == nil
}
