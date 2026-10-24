package tainter

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

func TestHot(t *testing.T) {
	pol := planner.Policy{Level: planner.LevelFor(drsv1alpha1.ThresholdDefault), TargetHeadroom: 0.8}
	nodes := []planner.Node{
		{Name: "lowscore", AllocatableCPU: 4, VMDemandCPU: 1, Score: 40, HasScore: true},
		{Name: "busy", AllocatableCPU: 4, VMDemandCPU: 3.5, Score: 90, HasScore: true},
		{Name: "reserved", AllocatableCPU: 4, VMDemandCPU: 2, ReservedCPU: 1.5},
		{Name: "fine", AllocatableCPU: 4, VMDemandCPU: 1, Score: 95, HasScore: true},
	}
	hot := Hot(nodes, pol)
	for name, want := range map[string]bool{"lowscore": true, "busy": true, "reserved": true, "fine": false} {
		if hot[name] != want {
			t.Errorf("%s hot=%v want %v", name, hot[name], want)
		}
	}
}

func TestReconcileAddsAndRemoves(t *testing.T) {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	a := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	b := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{
		{Key: drsv1alpha1.TaintHot, Value: "true", Effect: corev1.TaintEffectPreferNoSchedule},
		{Key: "keep", Effect: corev1.TaintEffectNoSchedule},
	}}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(a, b).Build()
	ctx := context.Background()
	tainted, changed, err := Reconcile(ctx, c, []corev1.Node{*a, *b}, map[string]bool{"a": true}, true)
	if err != nil || changed != 2 || len(tainted) != 1 || tainted[0] != "a" {
		t.Fatalf("tainted=%v changed=%d err=%v", tainted, changed, err)
	}
	var got corev1.Node
	_ = c.Get(ctx, client.ObjectKey{Name: "b"}, &got)
	if len(got.Spec.Taints) != 1 || got.Spec.Taints[0].Key != "keep" {
		t.Fatalf("other taints must survive, got %+v", got.Spec.Taints)
	}
	// disabled: everything removed
	var na, nb corev1.Node
	_ = c.Get(ctx, client.ObjectKey{Name: "a"}, &na)
	_ = c.Get(ctx, client.ObjectKey{Name: "b"}, &nb)
	tainted, changed, err = Reconcile(ctx, c, []corev1.Node{na, nb}, map[string]bool{"a": true}, false)
	if err != nil || changed != 1 || len(tainted) != 0 {
		t.Fatalf("disable: tainted=%v changed=%d err=%v", tainted, changed, err)
	}
}
