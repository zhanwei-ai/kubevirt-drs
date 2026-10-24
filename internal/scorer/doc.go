// Package scorer reads the already-computed DRS series from Prometheus. It never evaluates the score
// formula itself: the heavy math runs once a minute in the PrometheusRule the chart ships (vmi:drs_score and
// its components), and the brain reads a handful of instant queries per cycle:
//
//	avg_over_time(vmi:drs_score[<smoothingWindow>])   the smoothed score per (name, namespace, node)
//	vmi:drs_cpu_ready:ratio                          host-caused: CPU contention
//	vmi:drs_mem_reclaim:ratio                         host-caused: balloon reclaim
//	kube_node_status_allocatable / cpu usage          free CPU per node for the target prediction
//
// The scorer also owns the three dependency probes behind the DRSPolicy conditions PrometheusReachable,
// ScoreRuleLoaded (vmi:drs_score has series) and the raw-metric probe (kubevirt_vmi_vcpu_delay_seconds_total
// has series, i.e. KubeVirt is scraped at all).
package scorer
