// Package planner is the pure decision core: given scores, VMIs, nodes and the policy it returns zero or
// more proposed moves. It performs no I/O, so every decision is a table-driven unit test. One cycle:
//
//  1. gate: in-flight migrations (ALL VMIMs, not only ours) below the cluster limit; policy not paused;
//     dependencies Ready.
//  2. candidates: running, live-migratable VMIs not opted out (drs.zhanwei.ai/automation=disabled or the
//     descheduler's prefer-no-eviction), not in cooldown (last migration from VMI status, whoever started
//     it), whose smoothed score is below the level's MinScore AND whose low score is host-caused
//     (cpu_ready or balloon reclaim above the policy thresholds). A low score from inside the guest is
//     skipped: moving it would not help.
//  3. target: for each other node that fits (requests, nodeSelector/affinity, taints, CPU model labels,
//     NetworkAttachmentDefinitions, outbound limit of the source), predict the score after the move; take
//     the best.
//  4. cost/benefit: gain = predicted - current must exceed the level's MinGain; the target's own score may
//     not drop below the source's (no ping-pong); the (source, target) pair is not blacklisted.
//  5. at most one move per source node per cycle.
//
// Priority-1 moves (the VM violates a rule where it runs: fits(vmi, currentNode) is false) bypass the score
// gate and MinGain, like VMware's mandatory recommendations, but still obey cooldown and limits.
package planner
