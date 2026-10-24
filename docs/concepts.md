# Concepts: what DRS is, and how kubevirt-drs maps it onto KubeVirt

## The four steps

VMware DRS runs once a minute and, for every VM, answers four questions. Kubernetes splits them across
components; kubevirt-drs puts them back into one loop.

| question | VMware DRS | Kubernetes + descheduler | kubevirt-drs |
| --- | --- | --- | --- |
| should anything move? | VM DRS Score + migration threshold | node utilization vs thresholds | VM score + host-caused filter + cost/benefit |
| which VM? | the one whose score improves most | descheduler's pod order (priority, QoS) | lowest suffering score |
| to which host? | DRS, by predicted score | kube-scheduler, by requests | predicted score on each fitting node |
| how? | vMotion | eviction → KubeVirt live migration | `VirtualMachineInstanceMigration` with `addedNodeSelector` |

## Vocabulary mapping

| VMware | kubevirt-drs |
| --- | --- |
| VM DRS Score (0..100, "execution efficiency") | `vmi:drs_score` |
| Cluster DRS Score | `cluster:drs_score:avg` (vCPU-weighted) |
| Automation level Manual / Partially / Fully | `DRSPolicy.spec.automationLevel` Manual / FullyAutomated (initial placement is always automatic on Kubernetes, so Partially = Manual) |
| Migration threshold 1..5 | `migrationThreshold` Conservative / Default / Aggressive (+ priority-1 mandatory moves) |
| VM override: automation Disabled | annotation `drs.zhanwei.ai/automation: disabled` (also honours `descheduler.alpha.kubernetes.io/prefer-no-eviction`) |
| migration recommendation / drmdump | `DRSRecommendation` |
| apply recommendation | `kubectl patch drsrec … '{"spec":{"approved":true}}'` |
| vMotion cost | `estimatedCostSeconds` (guest memory / observed transfer rate) |
| affinity rules | Kubernetes affinity / anti-affinity / taints; a VM violating one where it runs gets a priority-1 recommendation |
| maintenance mode | `kubectl drain` + KubeVirt `evictionStrategy: LiveMigrate` (not kubevirt-drs) |
| DPM, predictive DRS | not planned |

## Components

```
Prometheus ──(recording rules: vmi:drs_score …)──▶ scorer ──▶ planner ──▶ executor ──▶ DRSRecommendation
     ▲                                                                       │              │ approved
     │ scrapes virt-handler                                                  │              ▼
KubeVirt ◀────────────────────────── watchdog ◀── VirtualMachineInstanceMigration ◀────────┘
```

* **Score** lives in Prometheus: one `PrometheusRule`, evaluated every minute. The controller never
  computes it; it reads it. Heavy math once, cheap reads many.
* **scorer** reads the smoothed score and the host-caused components, and probes the three dependencies
  (Prometheus reachable, rules loaded, KubeVirt available) that gate every action.
* **planner** is pure: scores + VMIs + nodes + policy in, moves out. Every decision is a unit test.
* **executor** writes `DRSRecommendation`s, creates the VMIM for approved ones, stamps annotations, emits
  events. It holds no eviction or delete verb on pods.
* **watchdog** deletes a *brain-created* VMIM stuck in Scheduling (a target that did not fit after all),
  marks the recommendation Failed and blacklists the pair; it also flags external evacuations.

## What is deliberately not here

* No eviction path. Eviction is how a descheduler expresses "move this"; KubeVirt turns it into a
  migration asynchronously, which needs `EvictionsInBackground`, `request-evict-only` annotations and PDB
  429 handling. Creating the migration directly needs none of that, and cannot shut a VM down.
* No initial placement. kube-scheduler does that; the optional soft tainter only nudges it away from hot
  nodes.
* No container rebalancing. Run a descheduler for that, with `virt-launcher` pods excluded (see
  [coexist-with-descheduler.md](coexist-with-descheduler.md)).
