package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

// LabelVMI on a DRSRecommendation names the VMI it is about (for kubectl -l and the pending-check).
const LabelVMI = "drs.zhanwei.ai/vmi"

// Executor writes the objects a decision needs. It holds no eviction verb and never deletes a pod or VM.
type Executor struct {
	Client   client.Client
	Recorder events.EventRecorder
}

// Recommend records a decision as a DRSRecommendation. approved=true (FullyAutomated) lets the reconciler
// apply it at once; otherwise it waits for a human and expires after ttl.
func (e *Executor) Recommend(ctx context.Context, d planner.Decision, approved bool, ttl time.Duration, now time.Time) (*drsv1alpha1.DRSRecommendation, error) {
	rec := &drsv1alpha1.DRSRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      recName(d.VM.Name, now),
			Namespace: d.VM.Namespace,
			Labels:    map[string]string{LabelVMI: d.VM.Name},
		},
		Spec: drsv1alpha1.DRSRecommendationSpec{
			VMI:                  d.VM.Name,
			SourceNode:           d.VM.Node,
			TargetNode:           d.TargetNode,
			CurrentScore:         d.CurrentScore,
			ExpectedScore:        d.ExpectedScore,
			Reason:               d.Reason,
			Priority:             d.Priority,
			EstimatedCostSeconds: d.CostSeconds,
			Approved:             approved,
			ExpiresAt:            &metav1.Time{Time: now.Add(ttl)},
		},
	}
	if err := e.Client.Create(ctx, rec); err != nil {
		return nil, fmt.Errorf("create recommendation %s/%s: %w", rec.Namespace, rec.Name, err)
	}
	rec.Status.Phase = drsv1alpha1.PhasePending
	if err := e.Client.Status().Update(ctx, rec); err != nil {
		return rec, fmt.Errorf("set recommendation %s/%s Pending: %w", rec.Namespace, rec.Name, err)
	}
	return rec, nil
}

// Apply creates the VirtualMachineInstanceMigration for an approved, still-valid recommendation, stamps the
// VMI and emits an Event. It is idempotent: an existing migration of the same name is adopted.
func (e *Executor) Apply(ctx context.Context, rec *drsv1alpha1.DRSRecommendation, now time.Time) error {
	var vmi kubevirtv1.VirtualMachineInstance
	if err := e.Client.Get(ctx, types.NamespacedName{Namespace: rec.Namespace, Name: rec.Spec.VMI}, &vmi); err != nil {
		if apierrors.IsNotFound(err) {
			return e.fail(ctx, rec, "VMI no longer exists")
		}
		return err
	}
	if vmi.Status.Phase != kubevirtv1.Running {
		return e.fail(ctx, rec, fmt.Sprintf("VMI is %s, not Running", vmi.Status.Phase))
	}
	if vmi.Status.NodeName != rec.Spec.SourceNode {
		return e.fail(ctx, rec, fmt.Sprintf("VMI moved to %s since the recommendation was made", vmi.Status.NodeName))
	}
	name := migrationName(rec.Name)
	mig := &kubevirtv1.VirtualMachineInstanceMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: rec.Namespace,
			Labels:    map[string]string{drsv1alpha1.LabelRecommendation: rec.Name},
			Annotations: map[string]string{
				drsv1alpha1.AnnotationLastReason: rec.Spec.Reason,
			},
		},
		Spec: kubevirtv1.VirtualMachineInstanceMigrationSpec{
			VMIName:           rec.Spec.VMI,
			AddedNodeSelector: map[string]string{corev1.LabelHostname: rec.Spec.TargetNode},
		},
	}
	if err := e.Client.Create(ctx, mig); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			// KubeVirt's webhook rejects a migration of a VMI that is already migrating, not migratable, ...
			return e.fail(ctx, rec, "create migration: "+strings.TrimSpace(err.Error()))
		}
	}
	rec.Status.Phase = drsv1alpha1.PhaseApplied
	rec.Status.Migration = name
	rec.Status.AppliedAt = &metav1.Time{Time: now}
	rec.Status.Message = fmt.Sprintf("migration %s created, target %s", name, rec.Spec.TargetNode)
	meta.SetStatusCondition(&rec.Status.Conditions, metav1.Condition{
		Type: "MigrationCreated", Status: metav1.ConditionTrue, Reason: "Applied", Message: rec.Status.Message,
		ObservedGeneration: rec.Generation,
	})
	if err := e.Client.Status().Update(ctx, rec); err != nil {
		return fmt.Errorf("update recommendation %s/%s status: %w", rec.Namespace, rec.Name, err)
	}
	patch := client.MergeFrom(vmi.DeepCopy())
	if vmi.Annotations == nil {
		vmi.Annotations = map[string]string{}
	}
	vmi.Annotations[drsv1alpha1.AnnotationLastMigratedAt] = now.UTC().Format(time.RFC3339)
	vmi.Annotations[drsv1alpha1.AnnotationLastReason] = rec.Spec.Reason
	if err := e.Client.Patch(ctx, &vmi, patch); err != nil {
		return fmt.Errorf("annotate VMI %s/%s: %w", vmi.Namespace, vmi.Name, err)
	}
	if e.Recorder != nil {
		e.Recorder.Eventf(&vmi, nil, corev1.EventTypeNormal, "DRSMigration", "Migrate", "kubevirt-drs: live migration %s to %s (%s; score %d -> %d expected)",
			name, rec.Spec.TargetNode, rec.Spec.Reason, rec.Spec.CurrentScore, rec.Spec.ExpectedScore)
	}
	return nil
}

// SyncOutcome copies a finished migration's result onto its recommendation.
func (e *Executor) SyncOutcome(ctx context.Context, rec *drsv1alpha1.DRSRecommendation, mig *kubevirtv1.VirtualMachineInstanceMigration) error {
	switch mig.Status.Phase {
	case kubevirtv1.MigrationSucceeded:
		if meta.IsStatusConditionTrue(rec.Status.Conditions, "MigrationSucceeded") {
			return nil
		}
		msg := "migration succeeded"
		if ms := mig.Status.MigrationState; ms != nil && ms.StartTimestamp != nil && ms.EndTimestamp != nil {
			msg = fmt.Sprintf("migration succeeded in %s", ms.EndTimestamp.Sub(ms.StartTimestamp.Time).Round(time.Second))
		}
		rec.Status.Message = msg
		meta.SetStatusCondition(&rec.Status.Conditions, metav1.Condition{Type: "MigrationSucceeded", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: msg, ObservedGeneration: rec.Generation})
		return e.Client.Status().Update(ctx, rec)
	case kubevirtv1.MigrationFailed:
		if rec.Status.Phase == drsv1alpha1.PhaseFailed {
			return nil
		}
		return e.fail(ctx, rec, "migration failed (kubectl describe vmim "+mig.Name+")")
	}
	return nil
}

// Expire marks Pending, unapproved recommendations past their expiry as Expired. Returns how many it expired.
func (e *Executor) Expire(ctx context.Context, now time.Time) (int, error) {
	var list drsv1alpha1.DRSRecommendationList
	if err := e.Client.List(ctx, &list); err != nil {
		return 0, fmt.Errorf("list recommendations: %w", err)
	}
	n := 0
	for i := range list.Items {
		rec := &list.Items[i]
		if rec.Status.Phase != drsv1alpha1.PhasePending || rec.Spec.Approved || rec.Spec.ExpiresAt == nil || rec.Spec.ExpiresAt.After(now) {
			continue
		}
		rec.Status.Phase = drsv1alpha1.PhaseExpired
		rec.Status.Message = "not approved before expiry; the VM is re-evaluated every cycle"
		if err := e.Client.Status().Update(ctx, rec); err != nil {
			return n, fmt.Errorf("expire %s/%s: %w", rec.Namespace, rec.Name, err)
		}
		n++
	}
	return n, nil
}

// Pending returns the VMIs that have a Pending recommendation, and how many there are.
func (e *Executor) Pending(ctx context.Context) (map[planner.Key]bool, int, error) {
	var list drsv1alpha1.DRSRecommendationList
	if err := e.Client.List(ctx, &list); err != nil {
		return nil, 0, fmt.Errorf("list recommendations: %w", err)
	}
	out := map[planner.Key]bool{}
	for i := range list.Items {
		if list.Items[i].Status.Phase == drsv1alpha1.PhasePending || list.Items[i].Status.Phase == "" {
			out[planner.Key{Namespace: list.Items[i].Namespace, Name: list.Items[i].Spec.VMI}] = true
		}
	}
	return out, len(out), nil
}

// SyncScoreAnnotation writes drs.zhanwei.ai/score on the VMI when the score crosses a 5-point bucket.
func (e *Executor) SyncScoreAnnotation(ctx context.Context, vmi *kubevirtv1.VirtualMachineInstance, score float64) error {
	want := fmt.Sprintf("%d", int(score/5)*5)
	if vmi.Annotations[drsv1alpha1.AnnotationScore] == want {
		return nil
	}
	patch := client.MergeFrom(vmi.DeepCopy())
	if vmi.Annotations == nil {
		vmi.Annotations = map[string]string{}
	}
	vmi.Annotations[drsv1alpha1.AnnotationScore] = want
	return e.Client.Patch(ctx, vmi, patch)
}

func (e *Executor) fail(ctx context.Context, rec *drsv1alpha1.DRSRecommendation, msg string) error {
	rec.Status.Phase = drsv1alpha1.PhaseFailed
	rec.Status.Message = msg
	meta.SetStatusCondition(&rec.Status.Conditions, metav1.Condition{Type: "MigrationCreated", Status: metav1.ConditionFalse, Reason: "Failed", Message: msg, ObservedGeneration: rec.Generation})
	if err := e.Client.Status().Update(ctx, rec); err != nil {
		return fmt.Errorf("mark %s/%s Failed: %w", rec.Namespace, rec.Name, err)
	}
	return nil
}

func recName(vmi string, now time.Time) string {
	base := strings.ToLower(vmi)
	suffix := "-" + now.UTC().Format("20060102-150405")
	if len(base)+len(suffix) > 63 {
		base = base[:63-len(suffix)]
	}
	return strings.TrimSuffix(base, "-") + suffix
}

func migrationName(rec string) string {
	n := "drs-" + rec
	if len(n) > 63 {
		n = n[:63]
	}
	return strings.TrimSuffix(n, "-")
}
