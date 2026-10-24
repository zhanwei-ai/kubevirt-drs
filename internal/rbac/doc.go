// Package rbac carries the controller-gen RBAC markers for the manager's ClusterRole. It has no code.
// `make manifests` renders them to config/rbac/role.yaml and hack/sync-rbac.sh copies the rules into the
// Helm chart, so the chart and the kustomize manifests never drift.
//
// Why each rule exists:
//   - VMIs: list/watch to score and plan; patch for the score / last-migrated annotations.
//   - VMIMs: create to apply a recommendation; delete only ones carrying drs.zhanwei.ai/recommendation
//     (the watchdog); list/watch to count EVERY in-flight migration, including evacuations.
//   - kubevirts: read parallelMigrationsPerCluster / parallelOutboundMigrationsPerNode.
//   - migrationpolicies: read bandwidth / completion settings for the cost estimate.
//   - nodes: list/watch for fits; patch only for the optional soft tainter.
//   - pods: virt-launcher pods (label-filtered cache) for requests and the descheduler opt-out annotation.
//   - network-attachment-definitions: a target must offer the networks the VM is attached to.
//   - events: one per applied / failed recommendation.
//   - leases: leader election when more than one replica runs.
//
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstancemigrations,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=kubevirt.io,resources=kubevirts,verbs=get;list;watch
// +kubebuilder:rbac:groups=migrations.kubevirt.io,resources=migrationpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=drs.zhanwei.ai,resources=drspolicies;drsrecommendations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=drs.zhanwei.ai,resources=drspolicies/status;drsrecommendations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
package rbac
