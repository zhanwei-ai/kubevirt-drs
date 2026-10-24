package planner

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func node(name string, cores float64, vmDemand float64, score float64, taints ...corev1.Taint) Node {
	return Node{
		Name: name,
		Obj: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelHostname: name}},
			Spec:       corev1.NodeSpec{Taints: taints},
		},
		Ready:                  true,
		AllocatableCPU:         cores,
		AllocatableMemoryBytes: 64 << 30,
		RequestedCPU:           -1,
		RequestedMemoryBytes:   -1,
		VMDemandCPU:            vmDemand,
		Score:                  score,
		HasScore:               score >= 0,
	}
}

func launcher(vcpus int32) *corev1.Pod {
	return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "compute",
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(int64(vcpus)*100, resource.DecimalSI),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		}},
	}}}}
}

func vm(name, nodeName string, vcpus int32, demand, score, ready float64) VM {
	return VM{
		Key: Key{Namespace: "default", Name: name}, Node: nodeName,
		VCPUs: vcpus, GuestMemoryBytes: 4 << 30, CPUDemandCores: demand,
		Score: score, HasScore: true, CPUReady: ready,
		LiveMigratable: true, Pod: launcher(vcpus),
	}
}

func policy() Policy {
	return Policy{
		Level:              LevelFor(drsv1alpha1.ThresholdDefault),
		HostCausedCPUReady: 0.05, HostCausedReclaim: 0.10,
		Cooldown:     Cooldown{Base: 15 * time.Minute, Max: 6 * time.Hour, Window: 48 * time.Hour},
		ClusterLimit: 5, PerNodeLimit: 2, TargetHeadroom: 0.8, Now: now,
	}
}

// The Day-2 research case: node A runs VM1 (8 vCPU, CPU-bound database, high ready time) and VM2 (4 vCPU,
// idle). A node-utilization descheduler evicts "a" pod and may pick VM2. The planner must pick VM1 and send
// it to C, the node with the most headroom.
func TestMovesTheSufferingVMNotTheIdleOne(t *testing.T) {
	nodes := []Node{node("A", 16, 15.2, 52), node("B", 16, 4.8, 97), node("C", 16, 1.6, 99)}
	vms := []VM{
		vm("vm1", "A", 8, 7.6, 28, 0.18),
		vm("vm2", "A", 4, 0.4, 96, 0.01),
		vm("vm3", "B", 4, 2.4, 97, 0.01), vm("vm4", "B", 4, 2.4, 97, 0.01),
		vm("vm5", "C", 2, 1.6, 99, 0.00),
	}
	ds, skips := Plan(vms, nodes, policy())
	if len(ds) != 1 {
		t.Fatalf("want exactly one decision, got %d (%+v); skips %+v", len(ds), ds, skips)
	}
	d := ds[0]
	if d.VM.Name != "vm1" || d.TargetNode != "C" {
		t.Fatalf("want vm1 -> C, got %s -> %s (%s)", d.VM.Name, d.TargetNode, d.Reason)
	}
	if d.Priority != 2 || d.ExpectedScore <= d.CurrentScore {
		t.Fatalf("unexpected decision %+v", d)
	}
	for _, s := range skips {
		if s.VM.Name == "vm2" && s.Reason == "" {
			t.Fatalf("vm2 must be skipped with a reason")
		}
	}
}

// A VM that is slow because of its own memory (swap, page faults) scores low but is not host-caused; moving
// it would not help, so it is left alone.
func TestGuestCausedLowScoreIsNotMoved(t *testing.T) {
	nodes := []Node{node("A", 16, 3, 60), node("B", 16, 1, 99)}
	v := vm("swappy", "A", 2, 1.0, 35, 0.01)
	v.MemPressure, v.MemReclaim = 0.65, 0.0
	ds, skips := Plan([]VM{v}, nodes, policy())
	if len(ds) != 0 {
		t.Fatalf("expected no decision, got %+v", ds)
	}
	if len(skips) != 1 || skips[0].Reason == "" {
		t.Fatalf("expected one explained skip, got %+v", skips)
	}
}

func TestCooldownBlocksARecentlyMovedVM(t *testing.T) {
	nodes := []Node{node("A", 16, 15, 50), node("B", 16, 1, 99)}
	v := vm("vm1", "A", 8, 7.6, 28, 0.18)
	v.LastMigrationEnd = now.Add(-5 * time.Minute)
	v.LastMigrationDuration = 40 * time.Second
	v.MigrationsInWindow = 1
	if ds, _ := Plan([]VM{v}, nodes, policy()); len(ds) != 0 {
		t.Fatalf("expected cooldown skip, got %+v", ds)
	}
	v.LastMigrationEnd = now.Add(-16 * time.Minute)
	if ds, _ := Plan([]VM{v}, nodes, policy()); len(ds) != 1 {
		t.Fatalf("expected a decision after the cooldown, got none")
	}
}

func TestClusterLimitGatesTheWholeCycle(t *testing.T) {
	nodes := []Node{node("A", 16, 15, 50), node("B", 16, 1, 99)}
	pol := policy()
	pol.InFlight = 5
	ds, skips := Plan([]VM{vm("vm1", "A", 8, 7.6, 28, 0.18)}, nodes, pol)
	if len(ds) != 0 || len(skips) != 1 {
		t.Fatalf("expected the cluster-limit skip only, got %+v / %+v", ds, skips)
	}
}

func TestOnePerSourceNodeAndOutboundLimit(t *testing.T) {
	nodes := []Node{node("A", 16, 15.5, 30), node("B", 16, 1, 99), node("C", 16, 1, 99)}
	vms := []VM{vm("vm1", "A", 8, 7.6, 20, 0.30), vm("vm2", "A", 8, 7.6, 25, 0.25)}
	ds, _ := Plan(vms, nodes, policy())
	if len(ds) != 1 || ds[0].VM.Name != "vm1" {
		t.Fatalf("want one move from A (vm1, the lowest score), got %+v", ds)
	}
	nodes[0].OutboundInFlight = 2
	if ds, _ := Plan(vms, nodes, policy()); len(ds) != 0 {
		t.Fatalf("outbound limit must block, got %+v", ds)
	}
}

func TestTaintedTargetIsSkippedAndTolerationHonoured(t *testing.T) {
	maint := corev1.Taint{Key: "maintenance", Effect: corev1.TaintEffectNoSchedule}
	nodes := []Node{node("A", 16, 15.5, 30), node("B", 16, 1, 99, maint)}
	v := vm("vm1", "A", 8, 7.6, 20, 0.30)
	if ds, _ := Plan([]VM{v}, nodes, policy()); len(ds) != 0 {
		t.Fatalf("tainted B must not be a target, got %+v", ds)
	}
	v.Pod.Spec.Tolerations = []corev1.Toleration{{Key: "maintenance", Operator: corev1.TolerationOpExists}}
	if ds, _ := Plan([]VM{v}, nodes, policy()); len(ds) != 1 || ds[0].TargetNode != "B" {
		t.Fatalf("with a toleration B is fine, got %+v", ds)
	}
}

func TestOptOutNotMigratableAndPendingRecommendation(t *testing.T) {
	nodes := []Node{node("A", 16, 15.5, 30), node("B", 16, 1, 99)}
	for _, mutate := range []func(*VM){
		func(v *VM) { v.OptedOut = true },
		func(v *VM) { v.LiveMigratable = false },
		func(v *VM) { v.HasPendingRecommendation = true },
	} {
		v := vm("vm1", "A", 8, 7.6, 20, 0.30)
		mutate(&v)
		if ds, _ := Plan([]VM{v}, nodes, policy()); len(ds) != 0 {
			t.Fatalf("expected no decision, got %+v", ds)
		}
	}
}

// A NoSchedule taint added to the node a healthy VM runs on is a rule violation: priority-1 move even
// though the score is perfect and the gain is zero.
func TestRuleViolationIsMandatory(t *testing.T) {
	drain := corev1.Taint{Key: "node.kubernetes.io/unschedulable-soon", Effect: corev1.TaintEffectNoSchedule}
	nodes := []Node{node("A", 16, 2, 99, drain), node("B", 16, 1, 99)}
	ds, skips := Plan([]VM{vm("vm1", "A", 2, 1.0, 99, 0.0)}, nodes, policy())
	if len(ds) != 1 || ds[0].Priority != 1 || ds[0].TargetNode != "B" {
		t.Fatalf("want a priority-1 move to B, got %+v (skips %+v)", ds, skips)
	}
}

// A cordon (spec.unschedulable + the node.kubernetes.io/unschedulable taint) is not a rule violation: the
// healthy VM stays, no priority-1 move. Found on the lab cluster on 2026-10-24: during a cordon/uncordon
// squeeze the brain recommended moving an unrelated healthy VM off the cordoned node.
func TestCordonIsNotARuleViolation(t *testing.T) {
	cordon := corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule}
	nodes := []Node{node("A", 16, 2, 99, cordon), node("B", 16, 1, 99)}
	nodes[0].Ready = false // cordoned
	nodes[0].Obj.Spec.Unschedulable = true
	ds, _ := Plan([]VM{vm("vm1", "A", 2, 1.0, 99, 0.0)}, nodes, policy())
	if len(ds) != 0 {
		t.Fatalf("cordon must not produce a move, got %+v", ds)
	}
	// ... but a suffering VM on a cordoned node may still be load-balanced away.
	ds, _ = Plan([]VM{vm("vm2", "A", 8, 7.6, 28, 0.18)}, nodes, policy())
	if len(ds) != 1 || ds[0].Priority != 2 {
		t.Fatalf("want a priority-2 move for the suffering VM, got %+v", ds)
	}
}

func TestNoPingPongOntoAWorseNode(t *testing.T) {
	// B is the only fitting target but already less happy than A.
	nodes := []Node{node("A", 16, 14, 45), node("B", 16, 2, 30)}
	ds, skips := Plan([]VM{vm("vm1", "A", 8, 7.0, 28, 0.18)}, nodes, policy())
	if len(ds) != 0 {
		t.Fatalf("expected ping-pong guard, got %+v", ds)
	}
	if len(skips) != 1 {
		t.Fatalf("want one skip, got %+v", skips)
	}
}

func TestConservativeNeedsMoreGain(t *testing.T) {
	nodes := []Node{node("A", 16, 15, 50), node("B", 16, 1, 99)}
	v := vm("vm1", "A", 8, 7.6, 45, 0.08) // predicted 100 -> gain 55
	pol := policy()
	pol.Level = LevelFor(drsv1alpha1.ThresholdConservative) // candidate below 50, gain >= 30: ok
	if ds, _ := Plan([]VM{v}, nodes, pol); len(ds) != 1 {
		t.Fatalf("conservative should still move a 45-scoring VM with 55 gain, got none")
	}
	v.Score = 55 // above the conservative floor
	if ds, _ := Plan([]VM{v}, nodes, pol); len(ds) != 0 {
		t.Fatalf("55 is above the conservative floor, got %+v", ds)
	}
}

func TestEffectiveCooldown(t *testing.T) {
	c := Cooldown{Base: 15 * time.Minute, Max: 6 * time.Hour, Window: 48 * time.Hour}
	cases := []struct {
		last   time.Duration
		n      int
		expect time.Duration
	}{
		{30 * time.Second, 0, 15 * time.Minute},
		{30 * time.Second, 1, 15 * time.Minute},
		{20 * time.Minute, 1, 20 * time.Minute}, // longer migration than base
		{30 * time.Second, 2, 30 * time.Minute},
		{30 * time.Second, 4, 2 * time.Hour},
		{30 * time.Second, 10, 6 * time.Hour}, // capped
	}
	for _, tc := range cases {
		if got := EffectiveCooldown(tc.last, tc.n, c); got != tc.expect {
			t.Errorf("EffectiveCooldown(%s, %d) = %s, want %s", tc.last, tc.n, got, tc.expect)
		}
	}
}

func TestCostSeconds(t *testing.T) {
	v := VM{GuestMemoryBytes: 4 << 30}
	if got := CostSeconds(&v, Policy{BandwidthBytesPerSec: 64 << 20}); got != 64 {
		t.Fatalf("4 GiB at 64 MiB/s = 64 s, got %d", got)
	}
	if got := CostSeconds(&v, Policy{}); got != 32 {
		t.Fatalf("default bandwidth 128 MiB/s -> 32 s, got %d", got)
	}
}

func TestInFlightMigrationBlocksTheVM(t *testing.T) {
	nodes := []Node{node("A", 16, 15, 50), node("B", 16, 1, 99)}
	v := vm("vm1", "A", 8, 7.6, 20, 0.30)
	v.MigrationInFlight = true
	if ds, _ := Plan([]VM{v}, nodes, policy()); len(ds) != 0 {
		t.Fatalf("a VM being migrated must not be recommended again, got %+v", ds)
	}
}

func TestStaleRecommendations(t *testing.T) {
	nodes := []Node{node("A", 16, 15, 50), node("B", 16, 1, 99)}
	pol := policy()
	v := vm("vm1", "A", 8, 7.6, 28, 0.18)
	if r := Stale(&v, "A", "B", nodes, pol); r != "" {
		t.Fatalf("valid recommendation reported stale: %q", r)
	}
	moved := v
	moved.Node = "B"
	if r := Stale(&moved, "A", "B", nodes, pol); r == "" {
		t.Fatal("moved VM must be stale")
	}
	recovered := v
	recovered.Score, recovered.CPUReady = 92, 0.01
	if r := Stale(&recovered, "A", "B", nodes, pol); r == "" {
		t.Fatal("recovered VM must be stale")
	}
	full := nodes
	full[1].VMDemandCPU = 15 // target filled up meanwhile: no headroom, no gain
	if r := Stale(&v, "A", "B", full, pol); r == "" {
		t.Fatal("target without headroom must be stale")
	}
	if r := Stale(nil, "A", "B", nodes, pol); r == "" {
		t.Fatal("missing VM must be stale")
	}
}

// Two suffering VMs on different source nodes in one cycle: the second must not be sent to the node the
// first one already claimed when that would leave it without headroom (lab finding 2026-10-24).
func TestReservedCapacityKeepsTargetsApart(t *testing.T) {
	nodes := []Node{node("A", 4, 3.8, 30), node("B", 4, 3.8, 30), node("C", 4, 0.4, 99), node("D", 4, 1.0, 99)}
	vms := []VM{vm("vm1", "A", 2, 1.9, 25, 0.20), vm("vm2", "B", 2, 1.9, 28, 0.19)}
	ds, skips := Plan(vms, nodes, policy())
	if len(ds) != 2 {
		t.Fatalf("want two moves, got %+v (skips %+v)", ds, skips)
	}
	if ds[0].TargetNode == ds[1].TargetNode {
		t.Fatalf("both moves went to %s", ds[0].TargetNode)
	}
	// with D removed, C can hold only one of them under the 80 %% headroom rule: the second is skipped
	ds, _ = Plan(vms, nodes[:3], policy())
	if len(ds) != 1 {
		t.Fatalf("C has room for one VM only, got %+v", ds)
	}
}

func TestSettlingSourceIsNotASourceAgain(t *testing.T) {
	nodes := []Node{node("A", 16, 15, 50), node("B", 16, 1, 99)}
	nodes[0].SettlingUntil = now.Add(3 * time.Minute)
	v := vm("vm1", "A", 8, 7.6, 28, 0.18)
	if ds, _ := Plan([]VM{v}, nodes, policy()); len(ds) != 0 {
		t.Fatalf("settling node must not be a source, got %+v", ds)
	}
	// ... unless the move is mandatory
	drain := corev1.Taint{Key: "maintenance", Effect: corev1.TaintEffectNoSchedule}
	nodes[0].Obj.Spec.Taints = []corev1.Taint{drain}
	if ds, _ := Plan([]VM{v}, nodes, policy()); len(ds) != 1 || ds[0].Priority != 1 {
		t.Fatalf("mandatory move must ignore settling, got %+v", ds)
	}
}
