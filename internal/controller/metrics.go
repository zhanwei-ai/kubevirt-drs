package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	metricReady = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubevirt_drs_ready", Help: "1 when every dependency check passed in the last cycle and the brain may act.",
	})
	metricCycleDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "kubevirt_drs_cycle_duration_seconds", Help: "Wall time of one collect/decide/act cycle.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
	})
	metricCycles = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubevirt_drs_cycles_total", Help: "Cycles by result.",
	}, []string{"result"})
	metricRecommendations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubevirt_drs_recommendations_total", Help: "Recommendations created, by priority and whether pre-approved.",
	}, []string{"priority", "approved"})
	metricMigrations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubevirt_drs_migrations_total", Help: "Migrations the brain created, by outcome of the create call.",
	}, []string{"result"})
	metricClusterScore = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubevirt_drs_cluster_score", Help: "vCPU-weighted average DRS score of running VMs as of the last cycle (-1 = unknown).",
	})
	metricInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubevirt_drs_inflight_migrations", Help: "Live migrations in flight (any creator) as of the last cycle.",
	})
	metricCandidates = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubevirt_drs_vms_evaluated", Help: "Running VMIs the planner evaluated in the last cycle.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(metricReady, metricCycleDuration, metricCycles, metricRecommendations, metricMigrations, metricClusterScore, metricInFlight, metricCandidates)
}
