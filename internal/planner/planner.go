package planner

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Plan runs one decision cycle. It is pure: no I/O, deterministic for a given input (VMs are visited by
// ascending score, ties by key). At most one decision per source node per cycle.
func Plan(vms []VM, nodes []Node, pol Policy) ([]Decision, []Skip) {
	var decisions []Decision
	var skips []Skip
	skip := func(k Key, format string, a ...any) {
		skips = append(skips, Skip{VM: k, Reason: fmt.Sprintf(format, a...)})
	}

	if pol.ClusterLimit > 0 && pol.InFlight >= pol.ClusterLimit {
		skip(Key{}, "cluster migration limit reached (%d in flight, limit %d)", pol.InFlight, pol.ClusterLimit)
		return nil, skips
	}
	// work on a copy: reservations made during this cycle must not leak into the caller's slice
	nodes = append([]Node(nil), nodes...)
	byName := make(map[string]*Node, len(nodes))
	for i := range nodes {
		byName[nodes[i].Name] = &nodes[i]
	}
	// planned outbound moves per source node this cycle (added to OutboundInFlight)
	planned := map[string]int{}

	order := make([]*VM, 0, len(vms))
	for i := range vms {
		order = append(order, &vms[i])
	}
	sort.SliceStable(order, func(i, j int) bool {
		si, sj := effectiveScore(order[i]), effectiveScore(order[j])
		if si != sj {
			return si < sj
		}
		return order[i].String() < order[j].String()
	})

	for _, vm := range order {
		src := byName[vm.Node]
		if vm.Node == "" || src == nil {
			skip(vm.Key, "node %q unknown", vm.Node)
			continue
		}
		// priority 1: the VM violates a rule where it runs (admin taint added, labels changed, ...). A cordon
		// or a NotReady node is not a rule: drain and node_ha own those.
		mandatory := !ViolatesRules(vm, src).OK
		if !vm.HasScore && !mandatory {
			skip(vm.Key, "no score yet")
			continue
		}
		if !mandatory {
			if vm.Score >= float64(pol.Level.MinScore) {
				skip(vm.Key, "score %.0f >= %d", vm.Score, pol.Level.MinScore)
				continue
			}
			if vm.CPUReady < pol.HostCausedCPUReady && vm.MemReclaim < pol.HostCausedReclaim {
				skip(vm.Key, "low score is guest-caused (cpu_ready %.1f%%, reclaim %.0f%%): a move would not help", vm.CPUReady*100, vm.MemReclaim*100)
				continue
			}
		}
		if !vm.LiveMigratable {
			skip(vm.Key, "not live-migratable")
			continue
		}
		if vm.OptedOut {
			skip(vm.Key, "opted out by annotation")
			continue
		}
		if vm.HasPendingRecommendation {
			skip(vm.Key, "a pending recommendation already exists")
			continue
		}
		if vm.MigrationInFlight {
			skip(vm.Key, "a migration is in flight")
			continue
		}
		if in, left := InCooldown(vm, pol.Cooldown, pol.Now); in {
			skip(vm.Key, "in cooldown for another %s", left.Round(time.Second))
			continue
		}
		if planned[vm.Node] > 0 {
			skip(vm.Key, "another move already planned from %s this cycle", vm.Node)
			continue
		}
		if !mandatory && src.SettlingUntil.After(pol.Now) {
			skip(vm.Key, "%s is settling after a recent move (until %s)", vm.Node, src.SettlingUntil.UTC().Format(time.TimeOnly))
			continue
		}
		if pol.PerNodeLimit > 0 && src.OutboundInFlight+planned[vm.Node] >= pol.PerNodeLimit {
			skip(vm.Key, "outbound limit of %s reached (%d)", vm.Node, pol.PerNodeLimit)
			continue
		}

		// target: the fitting node with the best predicted score; ties -> most CPU headroom.
		var best *Node
		bestScore, bestRoom := -1.0, -1.0
		for i := range nodes {
			n := &nodes[i]
			if n.Name == vm.Node {
				continue
			}
			if exp, ok := pol.Blacklist[Pair{vm.Node, n.Name}]; ok && exp.After(pol.Now) {
				continue
			}
			if r := Fits(vm, n, true); !r.OK {
				continue
			}
			predicted, room := Predict(vm, n, pol)
			if predicted > bestScore || (predicted == bestScore && room > bestRoom) {
				best, bestScore, bestRoom = n, predicted, room
			}
		}
		if best == nil {
			skip(vm.Key, "no node fits")
			continue
		}
		current := vm.Score
		if !vm.HasScore {
			current = 100
		}
		gain := bestScore - current
		if !mandatory {
			if gain < float64(pol.Level.MinGain) {
				skip(vm.Key, "best target %s gains only %.0f points (< %d)", best.Name, gain, pol.Level.MinGain)
				continue
			}
			// no ping-pong: never move onto a node that is already less happy than the source.
			if best.HasScore && src.HasScore && best.Score < src.Score {
				skip(vm.Key, "target %s (node score %.0f) is already worse off than %s (%.0f)", best.Name, best.Score, vm.Node, src.Score)
				continue
			}
		}
		d := Decision{
			VM:            vm,
			TargetNode:    best.Name,
			CurrentScore:  int32(math.Round(current)),
			ExpectedScore: int32(math.Round(bestScore)),
			Priority:      2,
			CostSeconds:   CostSeconds(vm, pol),
			Reason:        reason(vm, src, best, mandatory),
		}
		if mandatory {
			d.Priority = 1
		}
		decisions = append(decisions, d)
		planned[vm.Node]++
		best.ReservedCPU += vm.CPUDemandCores // later candidates in this cycle see the target as fuller
	}
	return decisions, skips
}

// effectiveScore orders VMs: unknown scores last.
func effectiveScore(vm *VM) float64 {
	if !vm.HasScore {
		return 101
	}
	return vm.Score
}

// Predict estimates the VM's score on `target` and returns it with the target's CPU headroom after the move
// (share of allocatable still free). v1 model: if VM demand after the move stays under TargetHeadroom the
// host-caused penalties (CPU ready, balloon reclaim) vanish and only the guest-caused ones remain; otherwise
// the VM keeps its current score (no gain, a move there is pointless).
func Predict(vm *VM, target *Node, pol Policy) (score float64, headroom float64) {
	headroom = 1
	if target.AllocatableCPU > 0 {
		headroom = 1 - (target.VMDemandCPU+target.ReservedCPU+vm.CPUDemandCores)/target.AllocatableCPU
	}
	limit := pol.TargetHeadroom
	if limit <= 0 {
		limit = 0.8
	}
	if headroom < 1-limit {
		if vm.HasScore {
			return vm.Score, headroom
		}
		return 100, headroom
	}
	guestMem := vm.MemPressure - vm.MemReclaim
	if guestMem < 0 {
		guestMem = 0
	}
	if guestMem > 1 {
		guestMem = 1
	}
	netDrop := vm.NetDrop
	if netDrop < 0 {
		netDrop = 0
	}
	if netDrop > 1 {
		netDrop = 1
	}
	return 100 * (1 - guestMem) * (1 - netDrop), headroom
}

// CostSeconds estimates the migration time from guest memory and the bandwidth the cluster allows.
func CostSeconds(vm *VM, pol Policy) int32 {
	bw := pol.BandwidthBytesPerSec
	if bw <= 0 {
		bw = DefaultBandwidth
	}
	if vm.GuestMemoryBytes <= 0 {
		return 0
	}
	return int32(math.Ceil(float64(vm.GuestMemoryBytes) / bw))
}

func reason(vm *VM, src, dst *Node, mandatory bool) string {
	room := dst.AllocatableCPU - dst.VMDemandCPU - dst.ReservedCPU - vm.CPUDemandCores
	if mandatory {
		r := ViolatesRules(vm, src)
		return fmt.Sprintf("rule violated on %s (%s); %s fits with %.1f cores free after the move", src.Name, r.Reason, dst.Name, room)
	}
	cause := fmt.Sprintf("cpu_ready %.0f%%", vm.CPUReady*100)
	if vm.MemReclaim >= vm.CPUReady {
		cause = fmt.Sprintf("balloon reclaimed %.0f%% of guest memory", vm.MemReclaim*100)
	}
	return fmt.Sprintf("%s on %s; %s has %.1f cores free after the move", cause, src.Name, dst.Name, room)
}

// Stale says why a Pending recommendation for vm should be expired, or "" when it still holds. VMware
// regenerates its recommendations every round and drops the ones that no longer apply; a stale proposal
// left in Manual mode would move a VM that has recovered, or onto a node that has filled up meanwhile.
func Stale(vm *VM, sourceNode, targetNode string, nodes []Node, pol Policy) string {
	if vm == nil {
		return "VM is gone or no longer running"
	}
	if vm.Node != sourceNode {
		return fmt.Sprintf("VM is on %s now, not %s", vm.Node, sourceNode)
	}
	if vm.MigrationInFlight {
		return "" // being executed, leave it
	}
	var src, dst *Node
	for i := range nodes {
		switch nodes[i].Name {
		case sourceNode:
			src = &nodes[i]
		case targetNode:
			dst = &nodes[i]
		}
	}
	mandatory := src != nil && !ViolatesRules(vm, src).OK
	if !mandatory && vm.HasScore {
		if vm.Score >= float64(pol.Level.MinScore) {
			return fmt.Sprintf("score recovered to %.0f (floor %d)", vm.Score, pol.Level.MinScore)
		}
		if vm.CPUReady < pol.HostCausedCPUReady && vm.MemReclaim < pol.HostCausedReclaim {
			return "low score is no longer host-caused"
		}
	}
	if dst == nil {
		return "target node is gone"
	}
	// The cycle's reservations already count this recommendation's own VM on the target (a pending move
	// reserves its demand there, kube.ApplyReservations). Re-validating the same move must not add the VM a
	// second time: counted twice, any VM using more than 40 % of the target's cores pushed the predicted
	// headroom under the limit and expired its recommendation one cycle after it was made (lab, 2026-10-27).
	target := *dst
	target.ReservedCPU -= vm.CPUDemandCores
	if target.ReservedCPU < 0 {
		target.ReservedCPU = 0
	}
	if r := Fits(vm, &target, true); !r.OK {
		return "target no longer fits: " + r.Reason
	}
	if !mandatory {
		predicted, _ := Predict(vm, &target, pol)
		if gain := predicted - vm.Score; gain < float64(pol.Level.MinGain) {
			return fmt.Sprintf("target would gain only %.0f points now (< %d)", gain, pol.Level.MinGain)
		}
	}
	return ""
}
