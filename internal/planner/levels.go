package planner

import drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"

// Level is what a MigrationThreshold means in score points (0..100).
type Level struct {
	// MinScore: VMs scoring at or above this are never load-balancing candidates.
	MinScore int32
	// MinGain: predicted improvement (target score minus current score) a move must bring.
	MinGain int32
}

// Levels maps the three thresholds to numbers. Calibrated against VMware's rule of thumb that 5% CPU ready
// per vCPU is noticeable (score 80 with the default weight 4); revisit after lab calibration, see
// docs/calibration.md.
var Levels = map[drsv1alpha1.MigrationThreshold]Level{
	drsv1alpha1.ThresholdConservative: {MinScore: 50, MinGain: 30},
	drsv1alpha1.ThresholdDefault:      {MinScore: 70, MinGain: 15},
	drsv1alpha1.ThresholdAggressive:   {MinScore: 85, MinGain: 5},
}

// LevelFor returns the Level of a threshold, falling back to Default for an empty or unknown value.
func LevelFor(t drsv1alpha1.MigrationThreshold) Level {
	if l, ok := Levels[t]; ok {
		return l
	}
	return Levels[drsv1alpha1.ThresholdDefault]
}
