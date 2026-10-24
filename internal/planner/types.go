package planner

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Key identifies a VMI.
type Key struct {
	Namespace, Name string
}

func (k Key) String() string { return k.Namespace + "/" + k.Name }

// VM is everything the planner knows about one running VMI. Built by internal/kube from the VMI, its
// virt-launcher pod and the scorer's metrics; the planner never touches the API.
type VM struct {
	Key
	Node string
	// VCPUs and GuestMemoryBytes size the move (weighting, cost).
	VCPUs            int32
	GuestMemoryBytes int64
	// CPUDemandCores is the VM's recent CPU usage in cores (rate of kubevirt_vmi_cpu_usage_seconds_total).
	CPUDemandCores float64
	// Score is the smoothed vmi:drs_score (0..100). HasScore is false when Prometheus has no series yet.
	Score    float64
	HasScore bool
	// Components of the score (0..1). CPUReady and MemReclaim are the host-caused ones.
	CPUReady    float64
	MemReclaim  float64
	MemPressure float64
	NetDrop     float64
	// LiveMigratable mirrors the VMI condition; a VMI without it is never a candidate.
	LiveMigratable bool
	// OptedOut: drs.zhanwei.ai/automation=disabled or the descheduler's prefer-no-eviction annotation.
	OptedOut bool
	// Migration history from the VMI status (whoever started the migrations).
	LastMigrationEnd      time.Time
	LastMigrationDuration time.Duration
	MigrationsInWindow    int
	// Pod is the virt-launcher pod: its nodeSelector / affinity / tolerations / requests are exactly what
	// the target pod of a migration will carry. nil = unknown (no fit checks beyond readiness).
	Pod *corev1.Pod
	// HasPendingRecommendation: a Pending DRSRecommendation already exists for this VM.
	HasPendingRecommendation bool
	// MigrationInFlight: a VirtualMachineInstanceMigration for this VM is not finished (ours or anyone's).
	// Found on the lab cluster: without it the cycle after an Apply re-recommended the same VM, whose
	// score had not yet recovered because it was still being copied.
	MigrationInFlight bool
}

// Node is a schedulable target as the planner sees it.
type Node struct {
	Name string
	Obj  *corev1.Node
	// Ready = Ready condition True and not cordoned.
	Ready bool
	// Capacity in cores / bytes (allocatable), and what is already asked of it.
	AllocatableCPU         float64
	AllocatableMemoryBytes int64
	// RequestedCPU / RequestedMemoryBytes: sum of pod requests on the node (kube-state-metrics). Negative =
	// unknown, the resource fit check is then skipped.
	RequestedCPU         float64
	RequestedMemoryBytes int64
	// VMDemandCPU: cores the VMs on this node actually use right now.
	VMDemandCPU float64
	// Score is node:drs_score:avg; HasScore false = no VM on it (treated as 100).
	Score    float64
	HasScore bool
	// OutboundInFlight: migrations currently leaving this node (any creator).
	OutboundInFlight int
	// ReservedCPU: cores that proposed (pending) or in-flight moves will add to this node, counted before
	// the metrics show them. Keeps two VMs from being sent to the same target in consecutive cycles.
	ReservedCPU float64
	// SettlingUntil: a move left this node recently (in flight, or completed inside the settle window); the
	// remaining VMs' smoothed scores still reflect the old contention, so no further load-balancing move is
	// taken from it until then. Mandatory moves ignore it.
	SettlingUntil time.Time
}

// Pair is a (source, target) node pair, the unit of the watchdog's blacklist.
type Pair struct{ Source, Target string }

// Cooldown mirrors DRSPolicy.spec.cooldown.
type Cooldown struct {
	Base, Max, Window time.Duration
}

// Policy is the DRSPolicy plus the cluster state a cycle needs.
type Policy struct {
	Level Level
	// HostCausedCPUReady / HostCausedReclaim: below both, a low score is guest-caused and not acted on.
	HostCausedCPUReady float64
	HostCausedReclaim  float64
	Cooldown           Cooldown
	// ClusterLimit / PerNodeLimit = KubeVirt's parallel limits (or the policy's lower caps); InFlight = every
	// migration currently not finished, whoever created it.
	ClusterLimit int
	PerNodeLimit int
	InFlight     int
	// TargetHeadroom: a target counts as relieving CPU contention only if VM demand after the move stays
	// below this share of its allocatable CPU (0.8).
	TargetHeadroom float64
	// BandwidthBytesPerSec for the cost estimate (KubeVirt bandwidthPerMigration or an observed rate);
	// 0 = unknown, DefaultBandwidth is used.
	BandwidthBytesPerSec float64
	Blacklist            map[Pair]time.Time
	// SettleWindow is how long after a move left a node the node is not a source again (the scoring
	// smoothing window).
	SettleWindow time.Duration
	Now          time.Time
}

// DefaultBandwidth is assumed for the cost estimate when nothing better is known (128 MiB/s).
const DefaultBandwidth = 128 << 20

// Decision is one proposed move.
type Decision struct {
	VM            *VM
	TargetNode    string
	CurrentScore  int32
	ExpectedScore int32
	Priority      int32 // 1 = mandatory (rule violated where it runs), 2 = load balancing
	CostSeconds   int32
	Reason        string
}

// Skip explains why a VM produced no decision this cycle (debug output, metrics).
type Skip struct {
	VM     Key
	Reason string
}
