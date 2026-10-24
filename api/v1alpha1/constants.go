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

// Condition types reported on DRSPolicy.status.conditions.
const (
	// ConditionReady is True only when every dependency check below passed in the last cycle. While it is
	// False the brain scores and expires recommendations but never creates or applies one.
	ConditionReady = "Ready"
	// ConditionPrometheusReachable: the configured Prometheus answered an instant query.
	ConditionPrometheusReachable = "PrometheusReachable"
	// ConditionScoreRuleLoaded: vmi:drs_score has series. False usually means the Prometheus Operator's
	// ruleSelector did not pick up the PrometheusRule (set prometheusRule.additionalLabels), or KubeVirt's
	// ServiceMonitor is not scraped (kubevirt_vmi_vcpu_delay_seconds_total has no series).
	ConditionScoreRuleLoaded = "ScoreRuleLoaded"
	// ConditionKubeVirtAvailable: the kubevirt.io/v1 API is served and the KubeVirt CR is Deployed.
	ConditionKubeVirtAvailable = "KubeVirtAvailable"
	// ConditionExternalEvacuationsDetected is True when evacuation migrations (kubevirt-evacuation-*) exist
	// for VMIs whose node is not cordoned: a third party (typically a descheduler that does not exclude
	// virt-launcher pods) is moving VMs behind the brain's back. Informational; the brain keeps going.
	ConditionExternalEvacuationsDetected = "ExternalEvacuationsDetected"
)

// Annotations and labels the brain reads or writes.
const (
	// AnnotationAutomation on a VirtualMachine or VMI: "disabled" excludes the VM from any recommendation
	// (VMware: VM Override = Disabled). Any other value is ignored.
	AnnotationAutomation = "drs.zhanwei.ai/automation"
	// AnnotationScore on a VMI: the last score written by the brain (integer 0..100, only updated when it
	// crosses a 5-point bucket). Convenience for kubectl/Headlamp; Prometheus is the source of truth.
	AnnotationScore = "drs.zhanwei.ai/score"
	// AnnotationLastMigratedAt / AnnotationLastReason on a VMI: set when the brain applies a recommendation.
	AnnotationLastMigratedAt = "drs.zhanwei.ai/last-migrated-at"
	AnnotationLastReason     = "drs.zhanwei.ai/last-reason"
	// LabelRecommendation on every VirtualMachineInstanceMigration the brain creates: the name of the
	// DRSRecommendation. The watchdog only ever deletes VMIMs carrying this label.
	LabelRecommendation = "drs.zhanwei.ai/recommendation"
	// DeschedulerPreferNoEviction is the descheduler's opt-out annotation; the brain honours it too so one
	// annotation excludes a VM from both.
	DeschedulerPreferNoEviction = "descheduler.alpha.kubernetes.io/prefer-no-eviction"
	// PolicyName is the only allowed name of the DRSPolicy singleton.
	PolicyName = "default"
	// TaintHot is the PreferNoSchedule taint the soft tainter puts on hot nodes (value "true").
	TaintHot = "drs.zhanwei.ai/hot"
)
