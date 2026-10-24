package scorer

import (
	"context"
	"slices"
)

// Probe is the result of the dependency checks behind the DRSPolicy conditions PrometheusReachable and
// ScoreRuleLoaded. KubeVirtAvailable is checked by the controller against the API.
type Probe struct {
	Reachable bool
	// KubeVirtScraped: Prometheus has an `up == 1` target of KubeVirt's metrics service, or raw
	// kubevirt_vmi_* series. Without it the score rules evaluate to nothing.
	KubeVirtScraped bool
	// ScoreRuleLoaded: the rule group the chart installs is loaded (ruleSelector matched the PrometheusRule).
	ScoreRuleLoaded bool
	Err             error
}

// RunProbe never returns an error itself; failures are in the fields.
func RunProbe(ctx context.Context, q Querier) Probe {
	var p Probe
	if _, err := q.Query(ctx, "vector(1)"); err != nil {
		p.Err = err
		return p
	}
	p.Reachable = true
	if vec, err := q.Query(ctx, `count(up{job=~".*kubevirt.*"} == 1) or count(kubevirt_vmi_vcpu_delay_seconds_total)`); err == nil && len(vec) > 0 && float64(vec[0].Value) > 0 {
		p.KubeVirtScraped = true
	}
	groups, err := q.RuleGroups(ctx)
	if err != nil {
		p.Err = err
		return p
	}
	p.ScoreRuleLoaded = slices.Contains(groups, ScoreRuleGroup)
	return p
}
