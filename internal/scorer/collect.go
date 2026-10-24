package scorer

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/prometheus/common/model"

	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

// ScoreRuleGroup is the name of the recording-rule group the chart installs.
const ScoreRuleGroup = "kubevirt-drs.score"

// VMMetrics is what Prometheus knows about one VMI.
type VMMetrics struct {
	Node        string
	Score       float64
	HasScore    bool
	CPUReady    float64
	MemReclaim  float64
	MemPressure float64
	NetDrop     float64
	CPUDemand   float64 // cores
	VCPUs       int32
}

// Metrics is one cycle's read of the recording rules plus the node-level numbers the planner needs.
//
// VM is keyed by VMI and then by node: after a live migration the old node's series linger for the length
// of the smoothing window (avg_over_time keeps reading history Prometheus has already marked stale), so a
// VMI can have two entries for a few minutes. For(key, node) picks the one for the node the VMI runs on.
type Metrics struct {
	VM map[planner.Key]map[string]*VMMetrics
	// NodeVMDemand: cores used by the VMs on each node.
	NodeVMDemand map[string]float64
	// NodeScore: node:drs_score:avg where it exists.
	NodeScore map[string]float64
	// NodeRequestedCPU / NodeRequestedMemory: sum of pod requests per node (kube-state-metrics); a node
	// missing here has unknown requests and the planner skips the resource fit check for it.
	NodeRequestedCPU    map[string]float64
	NodeRequestedMemory map[string]float64
	// ObservedTransferRate: median bytes/s recent live migrations actually achieved (24h window), 0 when
	// no migration happened. Feeds the cost estimate together with the configured bandwidth cap.
	ObservedTransferRate float64
}

// Collect runs the handful of instant queries a cycle needs. Every query reads a recording rule or a cheap
// aggregate; the score formula itself is never evaluated here.
func Collect(ctx context.Context, q Querier, smoothing time.Duration) (*Metrics, error) {
	m := &Metrics{
		VM:                  map[planner.Key]map[string]*VMMetrics{},
		NodeVMDemand:        map[string]float64{},
		NodeScore:           map[string]float64{},
		NodeRequestedCPU:    map[string]float64{},
		NodeRequestedMemory: map[string]float64{},
	}
	win := model.Duration(smoothing).String()
	perVM := []struct {
		promql string
		set    func(v *VMMetrics, x float64)
	}{
		{fmt.Sprintf("avg_over_time(vmi:drs_score[%s])", win), func(v *VMMetrics, x float64) { v.Score, v.HasScore = x, true }},
		{"vmi:drs_cpu_ready:ratio", func(v *VMMetrics, x float64) { v.CPUReady = x }},
		{"vmi:drs_mem_reclaim:ratio", func(v *VMMetrics, x float64) { v.MemReclaim = x }},
		{"vmi:drs_mem_pressure:ratio", func(v *VMMetrics, x float64) { v.MemPressure = x }},
		{"vmi:drs_net_drop:ratio", func(v *VMMetrics, x float64) { v.NetDrop = x }},
		{"vmi:drs_vcpu:count", func(v *VMMetrics, x float64) { v.VCPUs = int32(x) }},
		{"sum by (name, namespace, node) (rate(kubevirt_vmi_cpu_usage_seconds_total[5m]))", func(v *VMMetrics, x float64) { v.CPUDemand = x }},
	}
	for _, pq := range perVM {
		vec, err := q.Query(ctx, pq.promql)
		if err != nil {
			return nil, err
		}
		for _, s := range vec {
			k := planner.Key{Namespace: string(s.Metric["namespace"]), Name: string(s.Metric["name"])}
			if k.Name == "" {
				continue
			}
			x := float64(s.Value)
			if math.IsNaN(x) || math.IsInf(x, 0) {
				continue
			}
			n := string(s.Metric["node"])
			byNode := m.VM[k]
			if byNode == nil {
				byNode = map[string]*VMMetrics{}
				m.VM[k] = byNode
			}
			v := byNode[n]
			if v == nil {
				v = &VMMetrics{Node: n}
				byNode[n] = v
			}
			pq.set(v, x)
		}
	}
	perNode := []struct {
		promql string
		into   map[string]float64
	}{
		{"sum by (node) (rate(kubevirt_vmi_cpu_usage_seconds_total[5m]))", m.NodeVMDemand},
		{"node:drs_score:avg", m.NodeScore},
		{`sum by (node) (kube_pod_container_resource_requests{resource="cpu"})`, m.NodeRequestedCPU},
		{`sum by (node) (kube_pod_container_resource_requests{resource="memory"})`, m.NodeRequestedMemory},
	}
	for _, pq := range perNode {
		vec, err := q.Query(ctx, pq.promql)
		if err != nil {
			return nil, err
		}
		for _, s := range vec {
			n := string(s.Metric["node"])
			x := float64(s.Value)
			if n == "" || math.IsNaN(x) || math.IsInf(x, 0) {
				continue
			}
			pq.into[n] = x
		}
	}
	// kubevirt_vmi_migration_memory_transfer_rate_bytes has samples only while a migration runs; the
	// 24h avg_over_time per VMI is that VMI's recent average rate, the median across VMIs is the cluster's.
	vec, err := q.Query(ctx, `quantile(0.5, avg_over_time(kubevirt_vmi_migration_memory_transfer_rate_bytes[24h]) > 0)`)
	if err != nil {
		return nil, err
	}
	if len(vec) == 1 {
		if x := float64(vec[0].Value); x > 0 && !math.IsNaN(x) && !math.IsInf(x, 0) {
			m.ObservedTransferRate = x
		}
	}
	return m, nil
}

// For returns the metrics of a VMI on the node it currently runs on. When no entry carries that node
// (series not relabelled yet, or node unknown) and exactly one entry exists, that one is returned.
func (m *Metrics) For(key planner.Key, node string) *VMMetrics {
	byNode := m.VM[key]
	if len(byNode) == 0 {
		return nil
	}
	if v, ok := byNode[node]; ok {
		return v
	}
	if len(byNode) == 1 {
		for _, v := range byNode {
			return v
		}
	}
	return nil
}

// ClusterScore is the vCPU-weighted average over the given (VMI, node) pairs, or -1 when none has a score.
func (m *Metrics) ClusterScore(placement map[planner.Key]string) float64 {
	var num, den float64
	for key, node := range placement {
		v := m.For(key, node)
		if v == nil || !v.HasScore {
			continue
		}
		w := float64(v.VCPUs)
		if w <= 0 {
			w = 1
		}
		num += v.Score * w
		den += w
	}
	if den == 0 {
		return -1
	}
	return num / den
}
