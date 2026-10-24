package main

import "testing"

func TestAnswerCoversTheBrainsQueries(t *testing.T) {
	score := 20.0
	s := &Scenario{
		VMIs:  []VMI{{Namespace: "default", Name: "vm1", Node: "w1", Score: 20, CPUReady: 0.25, CPUDemand: 0.5, VCPUs: 1}},
		Nodes: map[string]Node{"w1": {VMDemand: 0.5, Score: &score}, "w2": {VMDemand: 0}},
	}
	cases := map[string]int{
		"vector(1)": 1,
		`count(up{job=~".*kubevirt.*"} == 1) or count(kubevirt_vmi_vcpu_delay_seconds_total)`: 1,
		"avg_over_time(vmi:drs_score[5m])": 1,
		"vmi:drs_cpu_ready:ratio":          1,
		"sum by (name, namespace, node) (rate(kubevirt_vmi_cpu_usage_seconds_total[5m]))": 1,
		"sum by (node) (rate(kubevirt_vmi_cpu_usage_seconds_total[5m]))":                  2,
		"node:drs_score:avg": 1,
		`sum by (node) (kube_pod_container_resource_requests{resource="cpu"})`: 0,
	}
	for q, want := range cases {
		if got := len(Answer(s, q)); got != want {
			t.Errorf("%s: %d samples, want %d", q, got, want)
		}
	}
	if v := Answer(s, "vmi:drs_cpu_ready:ratio")[0]; v.Metric["node"] != "w1" || v.Value[1] != "0.25" {
		t.Fatalf("unexpected sample %+v", v)
	}
}
