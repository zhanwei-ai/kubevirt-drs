package kube

import (
	"testing"
	"time"

	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

func TestApplyReservations(t *testing.T) {
	now := time.Date(2026, 10, 24, 14, 0, 0, 0, time.UTC)
	window := 5 * time.Minute
	vm1 := planner.Key{Namespace: "ns", Name: "vm1"}
	vm2 := planner.Key{Namespace: "ns", Name: "vm2"}
	vm3 := planner.Key{Namespace: "ns", Name: "vm3"}
	demand := func(k planner.Key) float64 { return map[planner.Key]float64{vm1: 1.9, vm2: 1.0, vm3: 0.5}[k] }
	nodes := []planner.Node{{Name: "A"}, {Name: "B"}, {Name: "C"}}
	res := []Reservation{
		{VMI: vm1, Source: "A", Target: "B", Pending: true},                   // proposal: reserves B, A keeps going
		{VMI: vm2, Source: "A", Target: "C"},                                  // in flight: reserves C, A settles
		{VMI: vm3, Source: "B", Target: "C", Done: now.Add(-2 * time.Minute)}, // done 2 min ago: still inside the window
		{VMI: vm3, Source: "C", Target: "A", Done: now.Add(-9 * time.Minute)}, // done 9 min ago: ignored
	}
	ApplyReservations(nodes, res, demand, now, window)
	if nodes[1].ReservedCPU != 1.9 || nodes[2].ReservedCPU != 1.5 || nodes[0].ReservedCPU != 0 {
		t.Fatalf("reserved A=%v B=%v C=%v", nodes[0].ReservedCPU, nodes[1].ReservedCPU, nodes[2].ReservedCPU)
	}
	if !nodes[0].SettlingUntil.Equal(now.Add(window)) {
		t.Fatalf("A (in-flight source) should settle until now+window, got %v", nodes[0].SettlingUntil)
	}
	if !nodes[1].SettlingUntil.Equal(now.Add(3 * time.Minute)) {
		t.Fatalf("B (source of a move done 2 min ago) should settle until done+window, got %v", nodes[1].SettlingUntil)
	}
	if !nodes[2].SettlingUntil.IsZero() {
		t.Fatalf("C's outbound move is outside the window: no settling, got %v", nodes[2].SettlingUntil)
	}
}
