# Limits and known risks

* **Prometheus Operator is a hard dependency (v1).** The score is a `PrometheusRule`; the brain reads
  Prometheus. A Prometheus-free mode (scraping virt-handler directly) is on the roadmap.
* **Silent failure mode: rules not loaded.** If the operator's `ruleSelector` does not match, the
  PrometheusRule exists but Prometheus never evaluates it. The brain reports `ScoreRuleLoaded=False` and
  refuses to act; set `prometheusRule.additionalLabels`.
* **The planner re-implements node fit.** `addedNodeSelector` can only narrow the VM's own constraints;
  a target the planner mis-judged (inter-pod affinity, topology spread, a network attachment only some nodes
  offer) leaves the VMIM in Scheduling. The watchdog deletes it after `spec.watchdog.gracePeriod` (2 min),
  marks the recommendation Failed and blacklists the pair for `spec.watchdog.blacklistFor` (1 h); that cycle
  is wasted but nothing else happens. The soft tainter nudges the scheduler for everything the brain does
  not pin.
* **Memory pressure is seen from the VM side.** Host memory pressure caused by containers shows up only
  once the balloon or the guest reacts. OpenShift's PSI-based profile sees it earlier; an optional PSI
  node signal is on the roadmap (needs `psi=1`).
* **No initial placement.** New VMs land where kube-scheduler puts them; the brain only moves them later.
* **No DPM, no predictive DRS.**
* **Calibration is per hardware generation.** The default weight 4 is VMware's rule of thumb, not a
  measurement of your cluster. See [calibration.md](calibration.md).
* **One cluster, one brain.** Multi-replica deployments need leader election; the chart refuses
  `replicas > 1` without it.
