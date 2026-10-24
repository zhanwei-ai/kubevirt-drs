# Coexisting with a descheduler

kubevirt-drs needs no descheduler. A descheduler is still useful for *containers* (rebalancing after a
node joins, `RemoveDuplicates`, `RemovePodsHavingTooManyRestarts`) and the two can share a cluster, as
long as only one of them moves VMs.

## Stock descheduler: nothing to do

The `DefaultEvictor` ships with `evictLocalStoragePods: false`, and every `virt-launcher` pod has emptyDir
volumes. A descheduler with default settings therefore never evicts a VM. Keep that default and you are
done.

## If you enabled `evictLocalStoragePods` (or run OpenShift's KubeVirt profile)

Exclude VM pods explicitly, so the descheduler keeps its container duties and kubevirt-drs owns VM
balancing:

```yaml
# docs/examples/descheduler-policy.yaml
pluginConfig:
  - name: DefaultEvictor
    args:
      labelSelector:
        matchExpressions:
          - {key: kubevirt.io, operator: NotIn, values: [virt-launcher]}
```

Without that, both will act on the same hot node: the descheduler evicts a VM picked by priority/QoS
order, the brain migrates the one with the worst score, both consume KubeVirt's parallel-migration slots,
and kube-scheduler may drop the evicted VM onto the node the brain just chose. Nothing breaks (KubeVirt
rejects a second migration of a VM already migrating; `evictionStrategy: LiveMigrate` keeps VMs alive), but
you pay for extra migrations and the score and the eviction counters will tell different stories.

## What the brain does regardless of your descheduler config

1. Counts **every** in-flight `VirtualMachineInstanceMigration` against the limits, not only its own.
2. Reads the cooldown from the VMI's `status.migrationState`: a VM a descheduler just moved is left alone.
3. Honours `descheduler.alpha.kubernetes.io/prefer-no-eviction` as an opt-out, next to its own
   `drs.zhanwei.ai/automation: disabled`.
4. Deletes only migrations carrying its own `drs.zhanwei.ai/recommendation` label; evacuations are never
   touched.
5. Sets the `ExternalEvacuationsDetected` condition and emits an event when `kubevirt-evacuation-*`
   migrations appear for VMIs on nodes that are **not** cordoned: the fingerprint of a descheduler
   evicting VMs.

## Hard guarantee (optional)

A `ValidatingAdmissionPolicy` cannot do this: on `pods/eviction` the admission object is the `Eviction`,
and CEL has no access to the target pod's labels. Kyverno can, with an API-call context:
[`examples/kyverno-eviction-guard.yaml`](examples/kyverno-eviction-guard.yaml) denies evictions of
`virt-launcher` pods requested by the descheduler's ServiceAccount, and nothing else. Untested against
every Kyverno version; treat it as a template.

## Using the score *with* a descheduler instead of the brain

If you want to keep the descheduler as the mover, `prometheusRule.deschedulerBridge.enabled` records
`node:drs_unhappiness:ratio` in the shape its Prometheus mode needs. You get "which node is unhappy"
from the score, but still the descheduler's choice of VM and kube-scheduler's choice of target.
