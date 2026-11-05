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
	return newExecutorWith(t, interceptor.Funcs{}, objs...)
}

// newExecutorWith lets a test put its own Get / Create in front of the fake client (a stale cache, a webhook).
func newExecutorWith(t *testing.T, hooks interceptor.Funcs, objs ...client.Object) (*Executor, client.Client) {
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
				if hooks.Get != nil {
					return hooks.Get(ctx, cl, key, obj, opts...)
				}
				return cl.Get(ctx, key, obj, opts...)
			},
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if hooks.Create != nil {
					return hooks.Create(ctx, cl, obj, opts...)
				}
				return cl.Create(ctx, obj, opts...)
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
	pending, err := ex.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].Spec.VMI != "vm1" {
		t.Fatalf("pending = %v %v", pending, err)
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

// FullyAutomated creates the recommendation pre-approved; the reconciler applies it on the create event while
// Recommend is still writing Pending, so the first status write of Apply can hit a conflict. It must re-read
// and retry, not leave a migration behind with the recommendation stuck in Pending (or Failed on the retry).
func TestApplyRetriesTheStatusWriteAfterAConflict(t *testing.T) {
	ctx := context.Background()
	ex, c := newExecutor(t, runningVMI("vm1", "A"))
	rec, err := ex.Recommend(ctx, decision("vm1"), true, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	stale := rec.DeepCopy() // what the reconciler read
	rec.Status.Message = "re-validated by the cycle"
	if err := c.Status().Update(ctx, rec); err != nil { // lands between the read and Apply's write
		t.Fatal(err)
	}
	if err := ex.Apply(ctx, stale, now); err != nil {
		t.Fatalf("Apply must survive the conflict: %v", err)
	}
	if stale.Status.Phase != drsv1alpha1.PhaseApplied || stale.Status.Migration == "" {
		t.Fatalf("not applied after the retry: %+v", stale.Status)
	}
	var got drsv1alpha1.DRSRecommendation
	if err := c.Get(ctx, client.ObjectKeyFromObject(rec), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != drsv1alpha1.PhaseApplied || got.Status.Migration != stale.Status.Migration || got.ResourceVersion != stale.ResourceVersion {
		t.Fatalf("stored %+v (rv %s) != returned %+v (rv %s)", got.Status, got.ResourceVersion, stale.Status, stale.ResourceVersion)
	}
	var migs kubevirtv1.VirtualMachineInstanceMigrationList
	if err := c.List(ctx, &migs, client.InNamespace("default")); err != nil || len(migs.Items) != 1 {
		t.Fatalf("exactly one migration expected: %d %v", len(migs.Items), err)
	}
}

// The retry after a lost status write may run against a cache that has not seen the first attempt's migration:
// its Create reaches KubeVirt's webhook, which refuses with "in-flight migration detected" (the webhook runs
// before the name-uniqueness check, so AlreadyExists never comes). Apply must then find its own migration
// through the API reader and adopt it, stamping the VMI as if it had created it now.
func TestApplyAdoptsTheMigrationKubeVirtSaysIsInFlight(t *testing.T) {
	ctx := context.Background()
	webhook, staleGets := false, 0
	hooks := interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*kubevirtv1.VirtualMachineInstanceMigration); ok && staleGets > 0 {
				staleGets--
				return apierrors.NewNotFound(kubevirtv1.Resource("virtualmachineinstancemigrations"), key.Name)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*kubevirtv1.VirtualMachineInstanceMigration); ok && webhook {
				return apierrors.NewForbidden(kubevirtv1.Resource("virtualmachineinstancemigrations"), obj.GetName(),
					fmt.Errorf("admission webhook \"migration-create-validator.kubevirt.io\" denied the request: in-flight migration detected. Active migration job (fec638bc) is currently already in progress for VMI vm1"))
			}
			return cl.Create(ctx, obj, opts...)
		},
	}
	ex, c := newExecutorWith(t, hooks, runningVMI("vm1", "A"))
	ex.Reader = c // the "uncached" read: same store, the stale cache is the one Get above
	rec, err := ex.Recommend(ctx, decision("vm1"), true, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	first := &kubevirtv1.VirtualMachineInstanceMigration{ // attempt 1's migration
		ObjectMeta: metav1.ObjectMeta{Name: migrationName(rec.Name), Namespace: "default", Labels: map[string]string{drsv1alpha1.LabelRecommendation: rec.Name}},
		Spec:       kubevirtv1.VirtualMachineInstanceMigrationSpec{VMIName: "vm1", AddedNodeSelector: map[string]string{corev1.LabelHostname: "C"}},
	}
	if err := c.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	webhook, staleGets = true, 1
	if err := ex.Apply(ctx, rec, now); err != nil {
		t.Fatalf("Apply must adopt its own migration: %v", err)
	}
	if rec.Status.Phase != drsv1alpha1.PhaseApplied || rec.Status.Migration != first.Name {
		t.Fatalf("not adopted: %+v", rec.Status)
	}
	var migs kubevirtv1.VirtualMachineInstanceMigrationList
	if err := c.List(ctx, &migs, client.InNamespace("default")); err != nil || len(migs.Items) != 1 {
		t.Fatalf("exactly one migration expected: %d %v", len(migs.Items), err)
	}
	var vmi kubevirtv1.VirtualMachineInstance
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "vm1"}, &vmi); err != nil {
		t.Fatal(err)
	}
	if vmi.Annotations[drsv1alpha1.AnnotationLastMigratedAt] == "" {
		t.Fatalf("VMI not stamped on adoption: %v", vmi.Annotations)
	}

	// a same-named migration that is NOT ours is never adopted: Apply reports it instead of claiming it
	ex2, c2 := newExecutorWith(t, hooks, runningVMI("vm1", "A"))
	ex2.Reader = c2
	webhook = false
	rec2, err := ex2.Recommend(ctx, decision("vm1"), true, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	foreign := &kubevirtv1.VirtualMachineInstanceMigration{
		ObjectMeta: metav1.ObjectMeta{Name: migrationName(rec2.Name), Namespace: "default"},
		Spec:       kubevirtv1.VirtualMachineInstanceMigrationSpec{VMIName: "vm1"},
	}
	if err := c2.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	webhook = true
	if err := ex2.Apply(ctx, rec2, now); err == nil || rec2.Status.Phase == drsv1alpha1.PhaseApplied {
		t.Fatalf("a foreign migration must not be adopted: err=%v status=%+v", err, rec2.Status)
	}
}

// The loser of the cycle-vs-reconciler race may retry only after the migration has finished: the VMI then
// sits on the target, which must not be mistaken for "VMI moved since the recommendation was made".
func TestApplyAdoptsEvenWhenTheVMIAlreadyMoved(t *testing.T) {
	ctx := context.Background()
	ex, c := newExecutor(t, runningVMI("vm1", "A"))
	rec, err := ex.Recommend(ctx, decision("vm1"), true, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	stale := rec.DeepCopy()
	if err := ex.Apply(ctx, rec, now); err != nil { // the winner
		t.Fatal(err)
	}
	var vmi kubevirtv1.VirtualMachineInstance
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "vm1"}, &vmi); err != nil {
		t.Fatal(err)
	}
	vmi.Status.NodeName = "C" // the migration ran (the harness keeps VMIs in its own store: plain Update)
	if err := c.Update(ctx, &vmi); err != nil {
		t.Fatal(err)
	}
	if err := ex.Apply(ctx, stale, now.Add(time.Minute)); err != nil { // the loser, late
		t.Fatal(err)
	}
	if stale.Status.Phase != drsv1alpha1.PhaseApplied || stale.Status.Migration != rec.Status.Migration {
		t.Fatalf("late retry must adopt, got %+v", stale.Status)
	}
	if !stale.Status.AppliedAt.Time.Equal(rec.Status.AppliedAt.Time) {
		t.Fatalf("late retry must keep the winner's AppliedAt: %v vs %v", stale.Status.AppliedAt, rec.Status.AppliedAt)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "vm1"}, &vmi); err != nil {
		t.Fatal(err)
	}
	if vmi.Annotations[drsv1alpha1.AnnotationLastMigratedAt] != now.UTC().Format(time.RFC3339) {
		t.Fatalf("the no-op retry must not re-stamp the VMI: %v", vmi.Annotations)
	}
}
