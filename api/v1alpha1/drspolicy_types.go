/*
Copyright The kubevirt-drs Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AutomationLevel is who presses the button, mirroring vSphere DRS. There is no PartiallyAutomated level:
// initial placement is always automatic in Kubernetes (kube-scheduler), so it would equal Manual.
// +kubebuilder:validation:Enum=Manual;FullyAutomated
type AutomationLevel string

const (
	// AutomationManual: the brain writes DRSRecommendations with approved=false and applies one as soon as
	// a human sets approved=true.
	AutomationManual AutomationLevel = "Manual"
	// AutomationFullyAutomated: the brain creates recommendations pre-approved and applies them itself.
	AutomationFullyAutomated AutomationLevel = "FullyAutomated"
)

// MigrationThreshold is how much predicted improvement (score points, 0..100) a move must bring to be worth
// a live migration, and below which score a VM is a candidate at all. See planner.Levels for the numbers.
// +kubebuilder:validation:Enum=Conservative;Default;Aggressive
type MigrationThreshold string

const (
	ThresholdConservative MigrationThreshold = "Conservative"
	ThresholdDefault      MigrationThreshold = "Default"
	ThresholdAggressive   MigrationThreshold = "Aggressive"
)

// CooldownSpec keeps the brain from moving the same VM again and again. Three layers, each can only raise
// the effective cooldown (same shape as OpenShift's KubevirtMigrationAware plugin): the base is the larger
// of Base and the duration of the VM's last migration; it doubles for every further migration completed
// inside HistoryWindow; it is capped at Max. The last migration is read from the VMI's
// status.migrationState, so a migration started by anyone (a drain, a descheduler) counts too.
type CooldownSpec struct {
	// +kubebuilder:default="15m"
	// +optional
	Base *metav1.Duration `json:"base,omitempty"`
	// +kubebuilder:default="6h"
	// +optional
	Max *metav1.Duration `json:"max,omitempty"`
	// +kubebuilder:default="48h"
	// +optional
	HistoryWindow *metav1.Duration `json:"historyWindow,omitempty"`
}

// ConcurrencySpec caps the in-flight migrations the brain tolerates before it stops acting for the cycle. It
// counts EVERY VirtualMachineInstanceMigration that is not finished, not only its own. Unset values are read
// from the KubeVirt CR (spec.configuration.migrations.parallelMigrationsPerCluster and
// parallelOutboundMigrationsPerNode; upstream defaults 5 and 2).
type ConcurrencySpec struct {
	// +kubebuilder:validation:Minimum=1
	// +optional
	Cluster *int32 `json:"cluster,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	PerNode *int32 `json:"perNode,omitempty"`
}

// SelectorSpec narrows which VMs the brain may move. Both empty = every running VMI.
type SelectorSpec struct {
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	// +optional
	VMSelector *metav1.LabelSelector `json:"vmSelector,omitempty"`
}

// ScoringSpec holds the decisions the planner takes on top of the Prometheus score. The score formula itself
// (weights, windows) lives in the PrometheusRule the chart ships; see docs/scoring.md.
type ScoringSpec struct {
	// HostCausedCPUReadyPercent: a VM whose vCPUs wait for a host core less than this share of the time is
	// not suffering from CPU contention on the host. Together with HostCausedReclaimPercent: below both, a
	// low score comes from inside the guest (its own RAM or IO) and moving it would not help, so the
	// planner skips it. This filter is the main difference to a node-utilization descheduler.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	HostCausedCPUReadyPercent *int32 `json:"hostCausedCPUReadyPercent,omitempty"`
	// HostCausedReclaimPercent: share of the guest's memory the balloon has taken back.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	HostCausedReclaimPercent *int32 `json:"hostCausedReclaimPercent,omitempty"`
	// SmoothingWindow is the avg_over_time window applied to the score before any decision.
	// +kubebuilder:default="5m"
	// +optional
	SmoothingWindow *metav1.Duration `json:"smoothingWindow,omitempty"`
}

// WatchdogSpec bounds the one failure mode of pinning a target: KubeVirt's addedNodeSelector can only narrow
// the VM's own constraints, so a target the planner mis-judged leaves the migration in Scheduling forever.
type WatchdogSpec struct {
	// GracePeriod a brain-created migration may stay in Pending/Scheduling before it is deleted, its
	// recommendation marked Failed and the (source, target) pair blacklisted.
	// +kubebuilder:default="2m"
	// +optional
	GracePeriod *metav1.Duration `json:"gracePeriod,omitempty"`
	// BlacklistFor is how long that pair is not proposed again.
	// +kubebuilder:default="1h"
	// +optional
	BlacklistFor *metav1.Duration `json:"blacklistFor,omitempty"`
}

// DRSPolicySpec configures the one DRS brain of a cluster.
type DRSPolicySpec struct {
	// +kubebuilder:default=Manual
	// +optional
	AutomationLevel AutomationLevel `json:"automationLevel,omitempty"`
	// +kubebuilder:default=Default
	// +optional
	MigrationThreshold MigrationThreshold `json:"migrationThreshold,omitempty"`
	// CycleInterval is how often the brain runs a collect / decide / act cycle. VMware DRS: one minute.
	// +kubebuilder:default="60s"
	// +optional
	CycleInterval *metav1.Duration `json:"cycleInterval,omitempty"`
	// +optional
	Cooldown CooldownSpec `json:"cooldown,omitempty"`
	// +optional
	Concurrency ConcurrencySpec `json:"concurrency,omitempty"`
	// +optional
	Selector SelectorSpec `json:"selector,omitempty"`
	// +optional
	Scoring ScoringSpec `json:"scoring,omitempty"`
	// +optional
	Watchdog WatchdogSpec `json:"watchdog,omitempty"`
	// RecommendationTTL: an unapproved recommendation older than this is marked Expired and the VM is
	// re-evaluated next cycle (VMware regenerates its recommendations every round too).
	// +kubebuilder:default="10m"
	// +optional
	RecommendationTTL *metav1.Duration `json:"recommendationTTL,omitempty"`
	// ScoreAnnotation: write the current score to the VMI annotation drs.zhanwei.ai/score (only when it
	// crosses a 5-point bucket). Turn off on large clusters to save VMI watch traffic.
	// +kubebuilder:default=true
	// +optional
	ScoreAnnotation *bool `json:"scoreAnnotation,omitempty"`
	// SoftTainter: put the PreferNoSchedule taint drs.zhanwei.ai/hot on nodes whose VMs score below the
	// threshold floor or whose VM demand exceeds the target headroom, so kube-scheduler also steers new VMs
	// (and the fallback placement of an unpinned migration) away from them. Removed when the node recovers.
	// +kubebuilder:default=false
	// +optional
	SoftTainter *bool `json:"softTainter,omitempty"`
	// Paused: keep scoring and expiring, but create no recommendation and apply nothing. Emergency brake.
	// +optional
	Paused bool `json:"paused,omitempty"`
}

// DRSPolicyStatus reports what the brain saw in its last cycle.
type DRSPolicyStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions: Ready, PrometheusReachable, ScoreRuleLoaded, KubeVirtAvailable, ExternalEvacuationsDetected.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	LastCycleTime *metav1.Time `json:"lastCycleTime,omitempty"`
	// +optional
	LastCycleDuration *metav1.Duration `json:"lastCycleDuration,omitempty"`
	// ClusterScore is the vCPU-weighted average score of all running VMs, 0..100, as of the last cycle.
	// +optional
	ClusterScore *int32 `json:"clusterScore,omitempty"`
	// +optional
	PendingRecommendations int32 `json:"pendingRecommendations,omitempty"`
	// +optional
	InFlightMigrations int32 `json:"inFlightMigrations,omitempty"`
	// HotNodes carry the soft taint right now (empty when softTainter is off).
	// +optional
	HotNodes []string `json:"hotNodes,omitempty"`
	// BlacklistedPairs: "source->target" pairs the watchdog has banned, with their expiry.
	// +optional
	BlacklistedPairs []string `json:"blacklistedPairs,omitempty"`
}

// DRSPolicy is the cluster-wide configuration of the DRS brain. It is a singleton named "default".
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=drsp
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="DRSPolicy is a singleton: the only allowed name is 'default'"
// +kubebuilder:printcolumn:name="Automation",type=string,JSONPath=`.spec.automationLevel`
// +kubebuilder:printcolumn:name="Threshold",type=string,JSONPath=`.spec.migrationThreshold`
// +kubebuilder:printcolumn:name="Paused",type=boolean,JSONPath=`.spec.paused`
// +kubebuilder:printcolumn:name="Score",type=integer,JSONPath=`.status.clusterScore`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type DRSPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DRSPolicySpec   `json:"spec,omitempty"`
	Status DRSPolicyStatus `json:"status,omitempty"`
}

// DRSPolicyList contains a list of DRSPolicy.
// +kubebuilder:object:root=true
type DRSPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DRSPolicy `json:"items"`
}
