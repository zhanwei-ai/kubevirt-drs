# Scoring

The score is a set of Prometheus recording rules (chart template `templates/prometheusrule.yaml`,
plain export `examples/drs-rules.yaml`). Every input is a raw metric exported by KubeVirt's virt-handler
with the labels `name`, `namespace`, `node`; the rules do not depend on KubeVirt's own
`vmi:kubevirt_vmi_*` rules (those exist only when the KubeVirt CR sets `monitorNamespace`, and have been
renamed between releases). Rules within the group evaluate in order, so each layer may use the one above.

## Layer 1: components, 0..1

| record | meaning | VMware analogue | source metric |
| --- | --- | --- | --- |
| `vmi:drs_vcpu:count` | vCPUs of the VM | — | `kubevirt_vmi_vcpu_seconds_total` (one series per vCPU) |
| `vmi:drs_cpu_ready:ratio` | share of time the vCPUs were runnable but waiting for a host core, averaged per vCPU | CPU ready time | `rate(kubevirt_vmi_vcpu_delay_seconds_total[5m]) / vcpus` |
| `vmi:drs_mem_reclaim:ratio` | share of the guest's memory the balloon took back | memory reclamation (balloon) | `1 − actual_balloon_bytes / domain_bytes` |
| `vmi:drs_swap:ratio` | guest swap traffic, 1.0 at 10 MiB/s | memory reclamation (swap) | `rate(swap_in) + rate(swap_out)` |
| `vmi:drs_pgmajfault:ratio` | guest major page faults, 1.0 at 50/s | memory burstiness | `rate(kubevirt_vmi_memory_pgmajfault_total[5m])` |
| `vmi:drs_mem_pressure:ratio` | reclaim + swap + faults, capped at 1 | memory efficiency loss | the three above |
| `vmi:drs_net_drop:ratio` | dropped / total packets (rx + tx) | network efficiency loss | `kubevirt_vmi_network_*_packets_dropped_total` |

The network component is best-effort: libvirt reports interface counters only for interface bindings it
manages. A VM on a binding without them (seen on a Kube-OVN underlay VM) has no
`kubevirt_vmi_network_*` series; the score then treats `net_drop` as 0 (`or … * 0`) instead of vanishing.

Not modelled (VMware has them, KubeVirt cannot see them without PMU access): CPU cache behaviour and
hypervisor "tax". Guest run-queue length (`kubevirt_vmi_guest_load_1m`) needs the guest agent and is not
exported reliably, so it is not used.

## Layer 2: the VM score

```
vmi:drs_score = 100 × clamp_min(1 − W × cpu_ready, 0) × (1 − mem_pressure) × (1 − net_drop)
```

`W` = `prometheusRule.cpuReadyWeight`, default **4**: VMware's rule of thumb is that 5 % ready time per
vCPU is noticeable, so 5 % maps to 80 and 25 % to 0. Efficiencies multiply instead of adding, like the VM
DRS Score. The swap and page-fault parts reflect pressure *inside* the guest; they lower the displayed
score but the planner acts only on the host-caused parts (ready time, balloon reclaim).

## Layer 3: aggregates

```
node:drs_score:avg    = Σ(score × vcpus) / Σ(vcpus) by node
cluster:drs_score:avg = Σ(score × vcpus) / Σ(vcpus)
```

## Optional: descheduler bridge

`prometheusRule.deschedulerBridge.enabled` adds `node:drs_unhappiness:ratio`, shaped for descheduler's
`LowNodeUtilization` Prometheus mode: value in [0, 1], node name copied into the `instance` label, and one
sample for every node (`kube_node_info` fills empty nodes with 0 = "underutilized"). It only tells a
descheduler *which node* is unhappy; the descheduler still picks the pod its own way.

## Cardinality and cost

Per running VM: 7 component series + the score = 8 (4 after the planner's own reads are counted
per minute). Per node: 1 (+1 with the bridge). 100 VMs ≈ 1 000 samples a minute ≈ 1 MB/day on disk.
The rule evaluation is dominated by the `rate()` over one series per vCPU.
