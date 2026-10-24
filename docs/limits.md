# Limits and known risks

* **Prometheus Operator is a hard dependency (v1).** The score is a `PrometheusRule`; the brain reads
  Prometheus. A Prometheus-free mode (scraping virt-handler directly) is on the roadmap.
* **Silent failure mode: rules not loaded.** If the operator's `ruleSelector` does not match, the
  PrometheusRule exists but Prometheus never evaluates it. The brain reports `ScoreRuleLoaded=False` and
  refuses to act; set `prometheusRule.additionalLabels`.
* **The planner re-implements node fit.** `addedNodeSelector` can only narrow the VM's own constraints;
  a target the planner mis-judged leaves the VMIM in Scheduling. The watchdog cleans it up after 2 minutes
  and blacklists the pair for an hour, but that cycle is wasted. The soft tainter is the fallback path.
* **Memory pressure is seen from the VM side.** Host memory pressure caused by containers shows up only
  once the balloon or the guest reacts. OpenShift's PSI-based profile sees it earlier; an optional PSI
  node signal is on the roadmap (needs `psi=1`).
* **No initial placement.** New VMs land where kube-scheduler puts them; the brain only moves them later.
* **No DPM, no predictive DRS.**
* **Calibration is per hardware generation.** The default weight 4 is VMware's rule of thumb, not a
  measurement of your cluster. See [calibration.md](calibration.md).
* **One cluster, one brain.** Multi-replica deployments need leader election; the chart refuses
  `replicas > 1` without it.
