package executor

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

var now = time.Date(2026, 10, 24, 14, 30, 0, 0, time.UTC)

func newExecutor(t *testing.T, objs ...client.Object) (*Executor, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := drsv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := kubevirtv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	// KubeVirt's VMI type has uint64 fields and the fake client validates every write with
	// structured-merge-diff, which panics on them ("unsupported type: uint64"). VMIs therefore live in a
	// plain in-memory store behind interceptors; everything else goes through the fake tracker. A real API
	// server has no such problem.
	vmis := map[types.NamespacedName]*kubevirtv1.VirtualMachineInstance{}
	rest := make([]client.Object, 0, len(objs))
	for _, o := range objs {
		if v, ok := o.(*kubevirtv1.VirtualMachineInstance); ok {
			vmis[client.ObjectKeyFromObject(v)] = v.DeepCopy()
			continue
		}
		rest = append(rest, o)
	}
	storeVMI := func(obj client.Object) error {
		v := obj.(*kubevirtv1.VirtualMachineInstance)
		cp := v.DeepCopy()
		cp.ResourceVersion = fmt.Sprint(len(cp.ResourceVersion) + 1 + int(time.Now().UnixNano()%1000))
		vmis[client.ObjectKeyFromObject(v)] = cp
		cp.DeepCopyInto(v)
		return nil
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rest...).
		WithStatusSubresource(&drsv1alpha1.DRSRecommendation{}, &drsv1alpha1.DRSPolicy{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if v, ok := obj.(*kubevirtv1.VirtualMachineInstance); ok {
					stored, found := vmis[key]
					if !found {
						return apierrors.NewNotFound(kubevirtv1.Resource("virtualmachineinstances"), key.Name)
					}
					stored.DeepCopyInto(v)
					return nil
				}
				return cl.Get(ctx, key, obj, opts...)
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*kubevirtv1.VirtualMachineInstance); ok {
					return storeVMI(obj)
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*kubevirtv1.VirtualMachineInstance); ok {
					return storeVMI(obj)
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).Build()
	return &Executor{Client: c}, c
}

func runningVMI(name, node string) *kubevirtv1.VirtualMachineInstance {
	return &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status:     kubevirtv1.VirtualMachineInstanceStatus{Phase: kubevirtv1.Running, NodeName: node},
	}
}

func decision(name string) planner.Decision {
	return planner.Decision{
		VM:            &planner.VM{Key: planner.Key{Namespace: "default", Name: name}, Node: "A", GuestMemoryBytes: 4 << 30},
		TargetNode:    "C",
		CurrentScore:  28,
		ExpectedScore: 97,
		Priority:      2,
		CostSeconds:   64,
		Reason:        "cpu_ready 18% on A; C has 12.4 cores free after the move",
	}
}

func TestRecommendThenApplyCreatesPinnedMigration(t *testing.T) {
	ctx := context.Background()
	ex, c := newExecutor(t, runningVMI("vm1", "A"))

	rec, err := ex.Recommend(ctx, decision("vm1"), false, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status.Phase != drsv1alpha1.PhasePending || rec.Spec.Approved || !rec.Spec.ExpiresAt.Time.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("unexpected recommendation: %+v", rec)
	}
	pending, n, err := ex.Pending(ctx)
	if err != nil || n != 1 || !pending[planner.Key{Namespace: "default", Name: "vm1"}] {
		t.Fatalf("pending = %v %d %v", pending, n, err)
	}

	// a human approves
	rec.Spec.Approved = true
	if err := c.Update(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := ex.Apply(ctx, rec, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if rec.Status.Phase != drsv1alpha1.PhaseApplied || rec.Status.Migration == "" {
		t.Fatalf("not applied: %+v", rec.Status)
	}
	var mig kubevirtv1.VirtualMachineInstanceMigration
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: rec.Status.Migration}, &mig); err != nil {
		t.Fatal(err)
	}
	if mig.Spec.VMIName != "vm1" || mig.Spec.AddedNodeSelector[corev1.LabelHostname] != "C" {
		t.Fatalf("migration not pinned to the target: %+v", mig.Spec)
	}
	if mig.Labels[drsv1alpha1.LabelRecommendation] != rec.Name {
		t.Fatalf("migration must carry the recommendation label: %v", mig.Labels)
	}
	var vmi kubevirtv1.VirtualMachineInstance
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "vm1"}, &vmi); err != nil {
		t.Fatal(err)
	}
	if vmi.Annotations[drsv1alpha1.AnnotationLastMigratedAt] == "" || vmi.Annotations[drsv1alpha1.AnnotationLastReason] == "" {
		t.Fatalf("VMI not stamped: %v", vmi.Annotations)
	}
	// applying again is a no-op on the migration (already exists) and keeps the phase
	if err := ex.Apply(ctx, rec, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// outcome sync
	mig.Status.Phase = kubevirtv1.MigrationSucceeded
	start, end := metav1.NewTime(now), metav1.NewTime(now.Add(40*time.Second))
	mig.Status.MigrationState = &kubevirtv1.VirtualMachineInstanceMigrationState{StartTimestamp: &start, EndTimestamp: &end, Completed: true}
	if err := ex.SyncOutcome(ctx, rec, &mig); err != nil {
		t.Fatal(err)
	}
	if rec.Status.Message != "migration succeeded in 40s" {
		t.Fatalf("outcome message %q", rec.Status.Message)
	}
}

func TestApplyFailsWhenTheVMIMoved(t *testing.T) {
	ctx := context.Background()
	ex, _ := newExecutor(t, runningVMI("vm1", "B")) // recommendation says source A
	rec, err := ex.Recommend(ctx, decision("vm1"), true, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.Apply(ctx, rec, now); err != nil {
		t.Fatal(err)
	}
	if rec.Status.Phase != drsv1alpha1.PhaseFailed || rec.Status.Migration != "" {
		t.Fatalf("expected Failed without a migration, got %+v", rec.Status)
	}
}

func TestExpireOnlyTouchesUnapprovedPending(t *testing.T) {
	ctx := context.Background()
	ex, c := newExecutor(t, runningVMI("vm1", "A"), runningVMI("vm2", "A"))
	old, _ := ex.Recommend(ctx, decision("vm1"), false, 5*time.Minute, now.Add(-10*time.Minute))
	fresh, _ := ex.Recommend(ctx, decision("vm2"), false, 5*time.Minute, now)
	n, err := ex.Expire(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v", n, err)
	}
	var got drsv1alpha1.DRSRecommendation
	_ = c.Get(ctx, client.ObjectKeyFromObject(old), &got)
	if got.Status.Phase != drsv1alpha1.PhaseExpired {
		t.Fatalf("old recommendation should be Expired, is %s", got.Status.Phase)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(fresh), &got)
	if got.Status.Phase != drsv1alpha1.PhasePending {
		t.Fatalf("fresh recommendation should stay Pending, is %s", got.Status.Phase)
	}
}

func TestScoreAnnotationIsBucketed(t *testing.T) {
	ctx := context.Background()
	ex, c := newExecutor(t, runningVMI("vm1", "A"))
	var vmi kubevirtv1.VirtualMachineInstance
	_ = c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "vm1"}, &vmi)
	if err := ex.SyncScoreAnnotation(ctx, &vmi, 97.4); err != nil {
		t.Fatal(err)
	}
	rv := vmi.ResourceVersion
	if err := ex.SyncScoreAnnotation(ctx, &vmi, 96.1); err != nil { // same bucket 95: no write
		t.Fatal(err)
	}
	if vmi.ResourceVersion != rv || vmi.Annotations[drsv1alpha1.AnnotationScore] != "95" {
		t.Fatalf("expected one write with bucket 95, got rv %s->%s ann %v", rv, vmi.ResourceVersion, vmi.Annotations)
	}
}

func TestNames(t *testing.T) {
	long := "a-very-long-virtual-machine-name-that-goes-on-and-on-forever-and-ever"
	if n := recName(long, now); len(n) > 63 {
		t.Fatalf("recommendation name too long: %d", len(n))
	}
	if n := migrationName(recName(long, now)); len(n) > 63 {
		t.Fatalf("migration name too long: %d", len(n))
	}
	if recName("Web-01", now) != "web-01-20261024-143000" {
		t.Fatalf("got %q", recName("Web-01", now))
	}
}
