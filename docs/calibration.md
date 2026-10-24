# Calibration

The only tunable with real consequences is `cpuReadyWeight` (default 4) together with the planner's
levels (`internal/planner/levels.go`). Do not enable `FullyAutomated` before you have watched the score
under a load you understand.

## Method (one afternoon, three nodes)

1. Install the chart score-only. Confirm `vmi:drs_score` has series for every running VM.
2. Pick one node with N cores and `cpuAllocationRatio` ≥ 2 (KubeVirt default 10, so overcommit is easy).
3. Start two VMs on it (`hack/lab/stress-vm.yaml`: Fedora cloud image with `stress-ng` installed; cordon
   the other nodes while creating them, then uncordon; never use soft affinity, the scheduler would pull
   them back and fake a ping-pong).
4. In VM1 run `stress-ng --cpu <vcpus> --timeout 0`. Watch `vmi:drs_cpu_ready:ratio` for both VMs:
   nothing should move yet (one busy VM on an idle node has ready ≈ 0).
5. Start `stress-ng --cpu` in VM2 too, then a third VM, until the sum of busy vCPUs exceeds N. Ready time
   now climbs for *every* VM on the node. Note the ready ratio at which guest-side latency gets bad (run
   `sysbench cpu` or a simple loop with timestamps inside one VM).
6. Set `cpuReadyWeight` so that *that* ready ratio scores around 50: `W ≈ 0.5 / ready`. If latency got
   bad at 12 % ready, W ≈ 4. If only at 25 %, W ≈ 2.
7. Memory: give one VM less RAM than its working set (`stress-ng --vm 1 --vm-bytes 120%`) and confirm
   `vmi:drs_swap:ratio` / `vmi:drs_pgmajfault:ratio` rise while `vmi:drs_mem_reclaim:ratio` stays 0: that
   VM's low score is guest-caused and the planner must *not* propose a move for it.
8. Record the numbers and the KubeVirt / kernel versions in your runbook. They change with CPU generation.

## What "good" looks like

| state | expected score |
| --- | --- |
| idle or busy VM on an uncontended node | 95–100 |
| noticeable contention (VMware's 5 %) | ≈ 80 |
| latency clearly degraded | 40–60 |
| vCPUs mostly waiting | 0–20 |

## Levels

| `migrationThreshold` | candidate below | needs predicted gain | VMware analogue |
| --- | --- | --- | --- |
| Conservative | 50 | 30 | level 1–2 |
| Default | 70 | 15 | level 3 |
| Aggressive | 85 | 5 | level 4–5 |

Priority-1 recommendations (the VM violates a rule where it runs) ignore these two gates.
