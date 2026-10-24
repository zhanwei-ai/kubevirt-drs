package scorer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"

	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

type fakeQuerier struct {
	answers map[string]model.Vector // keyed by a substring of the query
	groups  []string
}

func (f fakeQuerier) Query(_ context.Context, q string) (model.Vector, error) {
	for k, v := range f.answers {
		if strings.Contains(q, k) {
			return v, nil
		}
	}
	return nil, nil
}
func (f fakeQuerier) RuleGroups(context.Context) ([]string, error) { return f.groups, nil }

func sample(ns, name, node string, v float64) *model.Sample {
	return &model.Sample{Metric: model.Metric{"namespace": model.LabelValue(ns), "name": model.LabelValue(name), "node": model.LabelValue(node)}, Value: model.SampleValue(v)}
}

func TestCollectJoinsPerVMSeries(t *testing.T) {
	q := fakeQuerier{answers: map[string]model.Vector{
		"vmi:drs_score":           {sample("default", "a1", "n1", 42.5)},
		"vmi:drs_cpu_ready:ratio": {sample("default", "a1", "n1", 0.18)},
		"vmi:drs_vcpu:count":      {sample("default", "a1", "n1", 4)},
		"node:drs_score:avg":      {{Metric: model.Metric{"node": "n1"}, Value: 42.5}},
	}, groups: []string{ScoreRuleGroup}}
	m, err := Collect(context.Background(), q, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	key := planner.Key{Namespace: "default", Name: "a1"}
	v := m.For(key, "n1")
	if v == nil || !v.HasScore || v.Score != 42.5 || v.CPUReady != 0.18 || v.VCPUs != 4 || v.Node != "n1" {
		t.Fatalf("bad VM metrics: %+v", v)
	}
	if m.For(key, "other") == nil {
		t.Fatal("a single entry must be returned for an unknown node")
	}
	if m.NodeScore["n1"] != 42.5 {
		t.Fatalf("node score missing: %+v", m.NodeScore)
	}
	if cs := m.ClusterScore(map[planner.Key]string{key: "n1"}); cs != 42.5 {
		t.Fatalf("cluster score %v", cs)
	}
	p := RunProbe(context.Background(), q)
	if !p.Reachable || !p.ScoreRuleLoaded {
		t.Fatalf("probe %+v", p)
	}
}

// After a live migration the smoothed score has one series per node for a few minutes; the entry of the
// node the VMI runs on must win, never "the last sample seen".
func TestForPicksTheCurrentNodeAfterAMigration(t *testing.T) {
	q := fakeQuerier{answers: map[string]model.Vector{
		"vmi:drs_score": {sample("default", "vm1", "old", 18.2), sample("default", "vm1", "new", 97.1)},
	}}
	m, err := Collect(context.Background(), q, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	key := planner.Key{Namespace: "default", Name: "vm1"}
	if v := m.For(key, "new"); v == nil || v.Score != 97.1 {
		t.Fatalf("want the new node's score, got %+v", v)
	}
	if v := m.For(key, "old"); v == nil || v.Score != 18.2 {
		t.Fatalf("want the old node's score, got %+v", v)
	}
	if m.For(key, "elsewhere") != nil {
		t.Fatal("two entries and no node match must return nil, not a guess")
	}
}
