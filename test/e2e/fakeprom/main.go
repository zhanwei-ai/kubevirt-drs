// Command fakeprom is a stand-in Prometheus for the end-to-end test. It answers the instant queries
// kubevirt-drs makes from a scenario file (re-read on every request, so the test changes it through a
// ConfigMap) and reports the score rule group as loaded. Nothing is computed: the point of the e2e is the
// wiring from "Prometheus says this VM suffers" to "the VM runs on another node", not the score formula.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Scenario is what the fake knows.
type Scenario struct {
	VMIs  []VMI           `json:"vmis"`
	Nodes map[string]Node `json:"nodes"`
	// RuleGroupLoaded defaults to true; false makes the brain report ScoreRuleLoaded=False.
	RuleGroupLoaded *bool `json:"ruleGroupLoaded,omitempty"`
}

// VMI is one VM's recorded series.
type VMI struct {
	Namespace   string  `json:"namespace"`
	Name        string  `json:"name"`
	Node        string  `json:"node"`
	Score       float64 `json:"score"`
	CPUReady    float64 `json:"cpuReady"`
	MemReclaim  float64 `json:"memReclaim"`
	MemPressure float64 `json:"memPressure"`
	NetDrop     float64 `json:"netDrop"`
	CPUDemand   float64 `json:"cpuDemand"`
	VCPUs       int     `json:"vcpus"`
}

// Node is one node's aggregates.
type Node struct {
	VMDemand float64  `json:"vmDemand"`
	Score    *float64 `json:"score,omitempty"`
}

type sample struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"`
}

func main() {
	path := flag.String("scenario", "/scenario/scenario.json", "scenario JSON, re-read on every request")
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()
	load := func() (*Scenario, error) {
		b, err := os.ReadFile(*path)
		if err != nil {
			return nil, err
		}
		var s Scenario
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		return &s, nil
	}
	http.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	http.HandleFunc("/api/v1/rules", func(w http.ResponseWriter, _ *http.Request) {
		s, err := load()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		groups := []map[string]any{}
		if s.RuleGroupLoaded == nil || *s.RuleGroupLoaded {
			groups = append(groups, map[string]any{"name": "kubevirt-drs.score", "file": "fake", "interval": 60, "rules": []any{}})
		}
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"groups": groups}})
	})
	http.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		s, err := load()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		q := r.Form.Get("query")
		res := Answer(s, q)
		log.Printf("query %q -> %d samples", q, len(res))
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": res}})
	})
	log.Printf("fakeprom listening on %s, scenario %s", *addr, *path)
	log.Fatal(http.ListenAndServe(*addr, nil)) //nolint:gosec // test helper
}

// Answer maps one of the brain's queries onto the scenario. Unknown queries return an empty vector, which
// is what a real Prometheus returns for a series nobody records.
func Answer(s *Scenario, q string) []sample {
	ts := float64(time.Now().Unix())
	mk := func(labels map[string]string, v float64) sample {
		return sample{Metric: labels, Value: [2]any{ts, strconv.FormatFloat(v, 'f', -1, 64)}}
	}
	vmLabels := func(v VMI) map[string]string {
		return map[string]string{"name": v.Name, "namespace": v.Namespace, "node": v.Node}
	}
	perVM := func(pick func(VMI) float64) []sample {
		out := []sample{}
		for _, v := range s.VMIs {
			out = append(out, mk(vmLabels(v), pick(v)))
		}
		return out
	}
	switch {
	case strings.Contains(q, "vector(1)"):
		return []sample{mk(map[string]string{}, 1)}
	case strings.HasPrefix(q, "count(up{"):
		return []sample{mk(map[string]string{}, 1)}
	case strings.Contains(q, "vmi:drs_score"):
		return perVM(func(v VMI) float64 { return v.Score })
	case strings.Contains(q, "vmi:drs_cpu_ready:ratio"):
		return perVM(func(v VMI) float64 { return v.CPUReady })
	case strings.Contains(q, "vmi:drs_mem_reclaim:ratio"):
		return perVM(func(v VMI) float64 { return v.MemReclaim })
	case strings.Contains(q, "vmi:drs_mem_pressure:ratio"):
		return perVM(func(v VMI) float64 { return v.MemPressure })
	case strings.Contains(q, "vmi:drs_net_drop:ratio"):
		return perVM(func(v VMI) float64 { return v.NetDrop })
	case strings.Contains(q, "vmi:drs_vcpu:count"):
		return perVM(func(v VMI) float64 { return float64(v.VCPUs) })
	case strings.Contains(q, "kubevirt_vmi_cpu_usage_seconds_total") && strings.Contains(q, "by (node)"):
		out := []sample{}
		for name, n := range s.Nodes {
			out = append(out, mk(map[string]string{"node": name}, n.VMDemand))
		}
		return out
	case strings.Contains(q, "kubevirt_vmi_cpu_usage_seconds_total"):
		return perVM(func(v VMI) float64 { return v.CPUDemand })
	case strings.Contains(q, "node:drs_score:avg"):
		out := []sample{}
		for name, n := range s.Nodes {
			if n.Score != nil {
				out = append(out, mk(map[string]string{"node": name}, *n.Score))
			}
		}
		return out
	}
	return []sample{}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
