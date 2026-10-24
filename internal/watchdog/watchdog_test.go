package watchdog

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/executor"
	"github.com/zhanwei-ai/kubevirt-drs/internal/kube"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

var now = time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)

func TestStalled(t *testing.T) {
	base := kube.Move{Name: "drs-x", Recommendation: "x", InFlight: true, Phase: kubevirtv1.MigrationScheduling, Created: now.Add(-3 * time.Minute)}
	if !Stalled(base, now, 2*time.Minute) {
		t.Fatal("3 min in Scheduling with a 2 min grace must be stalled")
	}
	fresh := base
	fresh.Created = now.Add(-30 * time.Second)
	if Stalled(fresh, now, 2*time.Minute) {
		t.Fatal("inside the grace period is not stalled")
	}
	running := base
	running.Phase = kubevirtv1.MigrationRunning
	if Stalled(running, now, 2*time.Minute) {
		t.Fatal("a Running migration is never stalled by the watchdog")
	}
	foreign := base
	foreign.Recommendation = ""
	if Stalled(foreign, now, 2*time.Minute) {
		t.Fatal("migrations the brain did not create are never touched")
	}
}

func TestRunDeletesFailsAndBlacklists(t *testing.T) {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = drsv1alpha1.AddToScheme(s)
	_ = kubevirtv1.AddToScheme(s)
	rec := &drsv1alpha1.DRSRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: "vm1-x", Namespace: "default"},
		Spec:       drsv1alpha1.DRSRecommendationSpec{VMI: "vm1", SourceNode: "A", TargetNode: "C", Approved: true},
		Status:     drsv1alpha1.DRSRecommendationStatus{Phase: drsv1alpha1.PhaseApplied, Migration: "drs-vm1-x"},
	}
	mig := &kubevirtv1.VirtualMachineInstanceMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "drs-vm1-x", Namespace: "default", Labels: map[string]string{drsv1alpha1.LabelRecommendation: "vm1-x"}},
		Spec:       kubevirtv1.VirtualMachineInstanceMigrationSpec{VMIName: "vm1"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec, mig).WithStatusSubresource(&drsv1alpha1.DRSRecommendation{}).Build()
	var banned []planner.Pair
	w := &Watchdog{Client: c, Exec: &executor.Executor{Client: c}, Log: log.Log, Blacklist: func(p planner.Pair, _ time.Time) { banned = append(banned, p) }}
	moves := []kube.Move{{Name: "drs-vm1-x", Recommendation: "vm1-x", VMI: planner.Key{Namespace: "default", Name: "vm1"},
		Source: "A", Target: "C", Phase: kubevirtv1.MigrationScheduling, InFlight: true, Created: now.Add(-5 * time.Minute)}}
	n, err := w.Run(context.Background(), moves, now, 2*time.Minute, time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "drs-vm1-x"}, mig); !apierrors.IsNotFound(err) {
		t.Fatalf("migration should be deleted, got %v", err)
	}
	var got drsv1alpha1.DRSRecommendation
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vm1-x"}, &got)
	if got.Status.Phase != drsv1alpha1.PhaseFailed {
		t.Fatalf("recommendation should be Failed, is %s (%s)", got.Status.Phase, got.Status.Message)
	}
	if len(banned) != 1 || banned[0] != (planner.Pair{Source: "A", Target: "C"}) {
		t.Fatalf("pair not blacklisted: %+v", banned)
	}
}
