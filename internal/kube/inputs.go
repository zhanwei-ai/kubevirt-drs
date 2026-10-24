// Package kube turns API objects (VMIs, launcher pods, nodes, migrations, the KubeVirt CR) plus the scorer's
// metrics into the planner's inputs. It reads only; the executor writes.
package kube

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
	"github.com/zhanwei-ai/kubevirt-drs/internal/scorer"
)

// EvacuationPrefix is what KubeVirt names the migrations its eviction webhook triggers.
const EvacuationPrefix = "kubevirt-evacuation-"

// Limits are the cluster's live-migration limits, read from the KubeVirt CR (upstream defaults when unset).
type Limits struct {
	ParallelCluster      int
	ParallelNode         int
	BandwidthBytesPerSec float64 // 0 = unlimited / unknown
	KubeVirtDeployed     bool
}

// ReadLimits lists the KubeVirt CRs (there is one) and returns the migration limits.
func ReadLimits(ctx context.Context, c client.Reader) (Limits, error) {
	l := Limits{ParallelCluster: 5, ParallelNode: 2}
	var kvs kubevirtv1.KubeVirtList
	if err := c.List(ctx, &kvs); err != nil {
		return l, fmt.Errorf("list KubeVirt CRs: %w", err)
	}
	if len(kvs.Items) == 0 {
		return l, fmt.Errorf("no KubeVirt CR found")
	}
	kv := kvs.Items[0]
	l.KubeVirtDeployed = kv.Status.Phase == kubevirtv1.KubeVirtPhaseDeployed
	if mc := kv.Spec.Configuration.MigrationConfiguration; mc != nil {
		if mc.ParallelMigrationsPerCluster != nil {
			l.ParallelCluster = int(*mc.ParallelMigrationsPerCluster)
		}
		if mc.ParallelOutboundMigrationsPerNode != nil {
			l.ParallelNode = int(*mc.ParallelOutboundMigrationsPerNode)
		}
		if mc.BandwidthPerMigration != nil {
			l.BandwidthBytesPerSec = mc.BandwidthPerMigration.AsApproximateFloat64()
		}
	}
	return l, nil
}

// Move is one VirtualMachineInstanceMigration reduced to what the planner and the watchdog need.
type Move struct {
	Name           string
	Recommendation string // label drs.zhanwei.ai/recommendation, "" for migrations the brain did not create
	VMI            planner.Key
	Source, Target string
	Phase          kubevirtv1.VirtualMachineInstanceMigrationPhase
	InFlight       bool
	Created        time.Time
	End            time.Time // completed migrations only
}

// Migrations summarises the VirtualMachineInstanceMigrations of the cluster for one cycle.
type Migrations struct {
	// Moves: every migration (in flight or finished) with its source, target and timing.
	Moves []Move
	// InFlight: every migration not yet Succeeded or Failed, whoever created it.
	InFlight int
	// OutboundBySource: in-flight migrations per source node.
	OutboundBySource map[string]int
	// ExternalEvacuations: in-flight kubevirt-evacuation-* migrations whose source node is NOT cordoned:
	// something other than a drain is evicting VMs (a descheduler, typically).
	ExternalEvacuations int
	// CompletedByVMI: end timestamps of succeeded migrations per VMI (for the cooldown backoff).
	CompletedByVMI map[planner.Key][]time.Time
	// InFlightByVMI: VMIs with a migration not yet finished.
	InFlightByVMI map[planner.Key]bool
}

// ListMigrations reads all VMIMs. vmiNode maps a VMI to its current node (fallback for the source node of
// a migration that has not reported one yet); cordoned says whether a node is unschedulable.
func ListMigrations(ctx context.Context, c client.Reader, vmiNode map[planner.Key]string, cordoned map[string]bool) (*Migrations, error) {
	var list kubevirtv1.VirtualMachineInstanceMigrationList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	m := &Migrations{OutboundBySource: map[string]int{}, CompletedByVMI: map[planner.Key][]time.Time{}, InFlightByVMI: map[planner.Key]bool{}}
	for i := range list.Items {
		mig := &list.Items[i]
		key := planner.Key{Namespace: mig.Namespace, Name: mig.Spec.VMIName}
		mv := Move{Name: mig.Name, Recommendation: mig.Labels[drsv1alpha1.LabelRecommendation], VMI: key,
			Phase: mig.Status.Phase, Created: mig.CreationTimestamp.Time, Target: mig.Spec.AddedNodeSelector[corev1.LabelHostname]}
		if ms := mig.Status.MigrationState; ms != nil {
			mv.Source = ms.SourceNode
			if mv.Target == "" {
				mv.Target = ms.TargetNode
			}
			if ms.EndTimestamp != nil {
				mv.End = ms.EndTimestamp.Time
			}
		}
		if mv.Source == "" {
			mv.Source = vmiNode[key]
		}
		switch mig.Status.Phase {
		case kubevirtv1.MigrationSucceeded:
			if !mv.End.IsZero() {
				m.CompletedByVMI[key] = append(m.CompletedByVMI[key], mv.End)
			}
		case kubevirtv1.MigrationFailed:
		default:
			if mig.DeletionTimestamp != nil {
				continue
			}
			mv.InFlight = true
			m.InFlight++
			m.InFlightByVMI[key] = true
			if mv.Source != "" {
				m.OutboundBySource[mv.Source]++
			}
			if strings.HasPrefix(mig.Name, EvacuationPrefix) && mv.Source != "" && !cordoned[mv.Source] {
				m.ExternalEvacuations++
			}
		}
		m.Moves = append(m.Moves, mv)
	}
	return m, nil
}

// LauncherPods indexes the virt-launcher pods by the UID of the VMI that owns them (label kubevirt.io/created-by).
func LauncherPods(ctx context.Context, c client.Reader) (map[types.UID]*corev1.Pod, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.MatchingLabels{kubevirtv1.AppLabel: "virt-launcher"}); err != nil {
		return nil, fmt.Errorf("list virt-launcher pods: %w", err)
	}
	out := make(map[types.UID]*corev1.Pod, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		if uid := p.Labels[kubevirtv1.CreatedByLabel]; uid != "" {
			out[types.UID(uid)] = p
		}
	}
	return out, nil
}

// BuildOptions are the policy-derived knobs the input builder needs.
type BuildOptions struct {
	Now               time.Time
	CooldownWindow    time.Duration
	NamespaceSelector labels.Selector // nil = all
	VMSelector        labels.Selector // nil = all
	PendingRecs       map[planner.Key]bool
}

// BuildVMs converts running VMIs into planner VMs.
func BuildVMs(ctx context.Context, c client.Reader, vmis []kubevirtv1.VirtualMachineInstance, pods map[types.UID]*corev1.Pod,
	metrics *scorer.Metrics, migs *Migrations, opt BuildOptions) ([]planner.VM, error) {
	nsLabels := map[string]labels.Set{}
	var out []planner.VM
	for i := range vmis {
		vmi := &vmis[i]
		if vmi.Status.Phase != kubevirtv1.Running || vmi.DeletionTimestamp != nil {
			continue
		}
		if opt.NamespaceSelector != nil {
			set, ok := nsLabels[vmi.Namespace]
			if !ok {
				var ns corev1.Namespace
				if err := c.Get(ctx, types.NamespacedName{Name: vmi.Namespace}, &ns); err != nil {
					return nil, fmt.Errorf("get namespace %s: %w", vmi.Namespace, err)
				}
				set = ns.Labels
				nsLabels[vmi.Namespace] = set
			}
			if !opt.NamespaceSelector.Matches(set) {
				continue
			}
		}
		if opt.VMSelector != nil && !opt.VMSelector.Matches(labels.Set(vmi.Labels)) {
			continue
		}
		key := planner.Key{Namespace: vmi.Namespace, Name: vmi.Name}
		vm := planner.VM{
			Key:                      key,
			Node:                     vmi.Status.NodeName,
			GuestMemoryBytes:         guestMemory(vmi),
			VCPUs:                    specVCPUs(vmi),
			LiveMigratable:           conditionTrue(vmi, kubevirtv1.VirtualMachineInstanceIsMigratable),
			OptedOut:                 optedOut(vmi.Annotations),
			Pod:                      pods[vmi.UID],
			HasPendingRecommendation: opt.PendingRecs[key],
		}
		if metrics != nil {
			if mm := metrics.For(key, vmi.Status.NodeName); mm != nil {
				vm.Score, vm.HasScore = mm.Score, mm.HasScore
				vm.CPUReady, vm.MemReclaim, vm.MemPressure, vm.NetDrop = mm.CPUReady, mm.MemReclaim, mm.MemPressure, mm.NetDrop
				vm.CPUDemandCores = mm.CPUDemand
				if mm.VCPUs > 0 {
					vm.VCPUs = mm.VCPUs
				}
			}
		}
		if ms := vmi.Status.MigrationState; ms != nil && ms.Completed && !ms.Failed && ms.EndTimestamp != nil {
			vm.LastMigrationEnd = ms.EndTimestamp.Time
			if ms.StartTimestamp != nil {
				vm.LastMigrationDuration = ms.EndTimestamp.Sub(ms.StartTimestamp.Time)
			}
		}
		if migs != nil {
			vm.MigrationInFlight = migs.InFlightByVMI[key]
			if ms := vmi.Status.MigrationState; ms != nil && !ms.Completed && !ms.Failed {
				vm.MigrationInFlight = true
			}
			for _, end := range migs.CompletedByVMI[key] {
				if opt.CooldownWindow <= 0 || opt.Now.Sub(end) <= opt.CooldownWindow {
					vm.MigrationsInWindow++
				}
			}
			if !vm.LastMigrationEnd.IsZero() && vm.MigrationsInWindow == 0 && (opt.CooldownWindow <= 0 || opt.Now.Sub(vm.LastMigrationEnd) <= opt.CooldownWindow) {
				vm.MigrationsInWindow = 1
			}
		}
		out = append(out, vm)
	}
	return out, nil
}

// BuildNodes converts Nodes into planner Nodes.
func BuildNodes(nodes []corev1.Node, metrics *scorer.Metrics, migs *Migrations) []planner.Node {
	out := make([]planner.Node, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		pn := planner.Node{
			Name:                   n.Name,
			Obj:                    n,
			Ready:                  nodeReady(n) && !n.Spec.Unschedulable,
			AllocatableCPU:         quantityCores(n.Status.Allocatable[corev1.ResourceCPU]),
			AllocatableMemoryBytes: n.Status.Allocatable.Memory().Value(),
			RequestedCPU:           -1,
			RequestedMemoryBytes:   -1,
		}
		if metrics != nil {
			pn.VMDemandCPU = metrics.NodeVMDemand[n.Name]
			if s, ok := metrics.NodeScore[n.Name]; ok {
				pn.Score, pn.HasScore = s, true
			}
			if r, ok := metrics.NodeRequestedCPU[n.Name]; ok {
				pn.RequestedCPU = r
			}
			if r, ok := metrics.NodeRequestedMemory[n.Name]; ok {
				pn.RequestedMemoryBytes = int64(r)
			}
		}
		if migs != nil {
			pn.OutboundInFlight = migs.OutboundBySource[n.Name]
		}
		out = append(out, pn)
	}
	return out
}

// Cordoned maps node name -> spec.unschedulable.
func Cordoned(nodes []corev1.Node) map[string]bool {
	m := make(map[string]bool, len(nodes))
	for i := range nodes {
		m[nodes[i].Name] = nodes[i].Spec.Unschedulable
	}
	return m
}

// VMINodes maps running VMIs to their node.
func VMINodes(vmis []kubevirtv1.VirtualMachineInstance) map[planner.Key]string {
	m := make(map[planner.Key]string, len(vmis))
	for i := range vmis {
		m[planner.Key{Namespace: vmis[i].Namespace, Name: vmis[i].Name}] = vmis[i].Status.NodeName
	}
	return m
}

func optedOut(ann map[string]string) bool {
	if strings.EqualFold(ann[drsv1alpha1.AnnotationAutomation], "disabled") {
		return true
	}
	_, prefer := ann[drsv1alpha1.DeschedulerPreferNoEviction]
	return prefer
}

func conditionTrue(vmi *kubevirtv1.VirtualMachineInstance, t kubevirtv1.VirtualMachineInstanceConditionType) bool {
	for _, c := range vmi.Status.Conditions {
		if c.Type == t {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func guestMemory(vmi *kubevirtv1.VirtualMachineInstance) int64 {
	if vmi.Status.Memory != nil && vmi.Status.Memory.GuestCurrent != nil {
		return vmi.Status.Memory.GuestCurrent.Value()
	}
	if vmi.Spec.Domain.Memory != nil && vmi.Spec.Domain.Memory.Guest != nil {
		return vmi.Spec.Domain.Memory.Guest.Value()
	}
	if q, ok := vmi.Spec.Domain.Resources.Requests[corev1.ResourceMemory]; ok {
		return q.Value()
	}
	return 0
}

func specVCPUs(vmi *kubevirtv1.VirtualMachineInstance) int32 {
	if cpu := vmi.Spec.Domain.CPU; cpu != nil {
		n := uint32(1)
		if cpu.Sockets > 0 {
			n *= cpu.Sockets
		}
		if cpu.Cores > 0 {
			n *= cpu.Cores
		}
		if cpu.Threads > 0 {
			n *= cpu.Threads
		}
		if n > 0 {
			return int32(n)
		}
	}
	if q, ok := vmi.Spec.Domain.Resources.Requests[corev1.ResourceCPU]; ok {
		if v := q.Value(); v > 0 {
			return int32(v)
		}
	}
	return 1
}

func quantityCores(q resource.Quantity) float64 { return float64(q.MilliValue()) / 1000 }

// Duration returns d or def when d is nil.
func Duration(d *metav1.Duration, def time.Duration) time.Duration {
	if d == nil || d.Duration <= 0 {
		return def
	}
	return d.Duration
}

// Reservation is a proposed or running move the metrics do not show yet.
type Reservation struct {
	VMI            planner.Key
	Source, Target string
	// Done is when a completed move finished (zero for pending / in flight).
	Done time.Time
}

// ApplyReservations folds proposed and recent moves into the planner nodes: the target gets the VM's demand
// reserved, the source settles for `window` after the move (in-flight: from now). demand gives a VM's cores.
func ApplyReservations(nodes []planner.Node, res []Reservation, demand func(planner.Key) float64, now time.Time, window time.Duration) {
	byName := make(map[string]*planner.Node, len(nodes))
	for i := range nodes {
		byName[nodes[i].Name] = &nodes[i]
	}
	for _, r := range res {
		if !r.Done.IsZero() && now.Sub(r.Done) > window {
			continue
		}
		if t := byName[r.Target]; t != nil {
			t.ReservedCPU += demand(r.VMI)
		}
		if s := byName[r.Source]; s != nil && window > 0 {
			until := now.Add(window)
			if !r.Done.IsZero() {
				until = r.Done.Add(window)
			}
			if until.After(s.SettlingUntil) {
				s.SettlingUntil = until
			}
		}
	}
}
