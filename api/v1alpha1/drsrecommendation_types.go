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

// RecommendationPhase is the lifecycle of one recommendation.
// +kubebuilder:validation:Enum=Pending;Applied;Dismissed;Expired;Failed
type RecommendationPhase string

const (
	// PhasePending: waiting for approval (Manual) or for a free migration slot.
	PhasePending RecommendationPhase = "Pending"
	// PhaseApplied: a VirtualMachineInstanceMigration was created; status.migration names it.
	PhaseApplied RecommendationPhase = "Applied"
	// PhaseDismissed: a human set it aside (status.phase patched to Dismissed); not re-created while valid.
	PhaseDismissed RecommendationPhase = "Dismissed"
	// PhaseExpired: not approved within DRSPolicy.spec.recommendationTTL; the VM is re-evaluated next cycle.
	PhaseExpired RecommendationPhase = "Expired"
	// PhaseFailed: the migration failed or stalled and was cleaned up by the watchdog; see status.message.
	PhaseFailed RecommendationPhase = "Failed"
)

// DRSRecommendationSpec is one proposed live migration, with the numbers that justify it.
type DRSRecommendationSpec struct {
	// VMI is the VirtualMachineInstance to move (same namespace as this object).
	// +kubebuilder:validation:MinLength=1
	VMI string `json:"vmi"`
	// +kubebuilder:validation:MinLength=1
	SourceNode string `json:"sourceNode"`
	// +kubebuilder:validation:MinLength=1
	TargetNode string `json:"targetNode"`
	// CurrentScore and ExpectedScore are 0..100 (vmi:drs_score now, and predicted on TargetNode).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CurrentScore int32 `json:"currentScore"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	ExpectedScore int32 `json:"expectedScore"`
	// Reason is one human-readable line, e.g. "cpu_ready 18% on source; target has 2.6 cores free".
	Reason string `json:"reason"`
	// Priority: 1 = mandatory (the VM violates a rule where it runs: node affinity, taints), 2 = load
	// balancing. Conservative thresholds still apply priority-1 moves, like VMware's threshold level 1.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2
	// +optional
	Priority int32 `json:"priority,omitempty"`
	// EstimatedCostSeconds is the predicted migration time (guest memory / observed transfer rate).
	// +optional
	EstimatedCostSeconds int32 `json:"estimatedCostSeconds,omitempty"`
	// Approved is false until a human (Manual) or the brain (FullyAutomated) says go. Setting it to true on
	// a Pending recommendation makes the brain create the migration at once.
	Approved bool `json:"approved"`
	// ExpiresAt is when a still-unapproved recommendation turns Expired.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// DRSRecommendationStatus records what happened to the recommendation.
type DRSRecommendationStatus struct {
	// +kubebuilder:default=Pending
	// +optional
	Phase RecommendationPhase `json:"phase,omitempty"`
	// Migration is the VirtualMachineInstanceMigration created for this recommendation.
	// +optional
	Migration string `json:"migration,omitempty"`
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DRSRecommendation is one proposed or executed live migration.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=drsrec
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="VMI",type=string,JSONPath=`.spec.vmi`
// +kubebuilder:printcolumn:name="From",type=string,JSONPath=`.spec.sourceNode`
// +kubebuilder:printcolumn:name="To",type=string,JSONPath=`.spec.targetNode`
// +kubebuilder:printcolumn:name="Score",type=integer,JSONPath=`.spec.currentScore`
// +kubebuilder:printcolumn:name="Expected",type=integer,JSONPath=`.spec.expectedScore`
// +kubebuilder:printcolumn:name="Prio",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Approved",type=boolean,JSONPath=`.spec.approved`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type DRSRecommendation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DRSRecommendationSpec   `json:"spec,omitempty"`
	Status DRSRecommendationStatus `json:"status,omitempty"`
}

// DRSRecommendationList contains a list of DRSRecommendation.
// +kubebuilder:object:root=true
type DRSRecommendationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DRSRecommendation `json:"items"`
}
