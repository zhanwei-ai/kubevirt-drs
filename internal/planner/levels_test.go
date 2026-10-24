package planner

import (
	"testing"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
)

func TestLevelsAreOrdered(t *testing.T) {
	c, d, a := Levels[drsv1alpha1.ThresholdConservative], Levels[drsv1alpha1.ThresholdDefault], Levels[drsv1alpha1.ThresholdAggressive]
	if c.MinScore >= d.MinScore || d.MinScore >= a.MinScore {
		t.Fatalf("MinScore must grow Conservative < Default < Aggressive: %d %d %d", c.MinScore, d.MinScore, a.MinScore)
	}
	if c.MinGain <= d.MinGain || d.MinGain <= a.MinGain {
		t.Fatalf("MinGain must shrink Conservative > Default > Aggressive: %d %d %d", c.MinGain, d.MinGain, a.MinGain)
	}
}

func TestLevelForFallsBackToDefault(t *testing.T) {
	for _, in := range []drsv1alpha1.MigrationThreshold{"", "Bogus"} {
		if got := LevelFor(in); got != Levels[drsv1alpha1.ThresholdDefault] {
			t.Fatalf("LevelFor(%q) = %+v, want Default", in, got)
		}
	}
}
