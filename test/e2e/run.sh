#!/usr/bin/env bash
# End-to-end test on kind: KubeVirt in software emulation, a scripted stand-in for Prometheus, the chart with
# the controller. Three scenarios, in order:
#   1. FullyAutomated: Prometheus says vm1 suffers -> recommendation -> pinned migration -> vm1 runs on the
#      other node with the same UID, the source was hot-tainted -> healthy again, nothing more happens.
#   2. Manual: vm2 suffers -> the recommendation waits unapproved for two cycles (no migration) -> approved
#      by hand -> pinned migration -> vm2 on its target with the same UID.
#   3. Watchdog: an approved recommendation pinned to a node that does not exist -> the migration stays
#      Pending -> after the grace period it is deleted, the recommendation is Failed, the pair is blacklisted
#      with a Warning Event -> the blacklist survives a controller restart.
#
#   test/e2e/run.sh            # creates kind cluster kubevirt-drs-e2e, runs, deletes it (KEEP=1 keeps it)
#   KUBEVIRT_VERSION=v1.9.1 KIND_NODE_IMAGE=kindest/node:v1.34.0 test/e2e/run.sh
#
# Needs: kind, kubectl, helm, docker (or podman with KIND_EXPERIMENTAL_PROVIDER=podman).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${KIND_CLUSTER:-kubevirt-drs-e2e}"
KUBEVIRT_VERSION="${KUBEVIRT_VERSION:-v1.9.1}"
PROM_OPERATOR_VERSION="${PROM_OPERATOR_VERSION:-v0.94.1}"
IMAGE="ghcr.io/zhanwei-ai/kubevirt-drs:e2e"
FAKEPROM_IMAGE="ghcr.io/zhanwei-ai/kubevirt-drs-fakeprom:e2e"
ENGINE="${ENGINE:-docker}"
log() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }

diagnostics() {
  echo; echo "===== diagnostics ====="
  kubectl get nodes -o wide || true
  kubectl -n kubevirt-drs get deploy,pod || true
  kubectl get drspolicy default -o yaml 2>/dev/null | sed -n '/^status:/,$p' || true
  kubectl get drsrec -A -o wide || true
  kubectl get vmim -A -o yaml 2>/dev/null | grep -E 'name:|phase:|targetNode|sourceNode|addedNodeSelector|hostname' || true
  kubectl get vmi -A -o wide || true
  kubectl -n kubevirt-drs logs deploy/kubevirt-drs --tail=80 2>/dev/null || true
  kubectl logs deploy/fakeprom --tail=20 2>/dev/null || true
  kubectl get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -25 || true
}
cleanup() {
  rc=$?
  if [[ $rc -ne 0 ]]; then diagnostics; fi
  if [[ "${KEEP:-0}" != 1 ]]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  exit $rc
}
trap cleanup EXIT

wait_for() { # wait_for <seconds> <description> <command...>
  local t=$1 desc=$2; shift 2
  local end=$((SECONDS + t))
  until "$@" >/dev/null 2>&1; do
    [[ $SECONDS -lt $end ]] || die "timed out after ${t}s waiting for: $desc"
    sleep 5
  done
  echo "    ok: $desc ($((SECONDS))s)"
}
set_scenario() { # set_scenario <json>: update the ConfigMap and restart fakeprom so it is picked up at once
  kubectl create configmap fakeprom-scenario --from-literal=scenario.json="$1" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl rollout restart deploy/fakeprom >/dev/null
  kubectl rollout status deploy/fakeprom --timeout=120s >/dev/null
}

log "kind cluster $CLUSTER"
args=(--name "$CLUSTER" --config "$ROOT/test/e2e/kind.yaml" --wait 120s)
[[ -n "${KIND_NODE_IMAGE:-}" ]] && args+=(--image "$KIND_NODE_IMAGE")
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster "${args[@]}"
kubectl config use-context "kind-$CLUSTER" >/dev/null
read -r -a WORKERS <<< "$(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[*].metadata.name}')"
[[ ${#WORKERS[@]} -ge 2 ]] || die "need two worker nodes, got ${WORKERS[*]}"

log "images -> kind"
$ENGINE build -q -f "$ROOT/Containerfile" -t "$IMAGE" --build-arg VERSION=e2e "$ROOT" >/dev/null
$ENGINE build -q -f "$ROOT/Containerfile.fakeprom" -t "$FAKEPROM_IMAGE" "$ROOT" >/dev/null
kind load docker-image --name "$CLUSTER" "$IMAGE" "$FAKEPROM_IMAGE" >/dev/null

log "KubeVirt $KUBEVIRT_VERSION (software emulation)"
base="https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}"
kubectl apply -f "$base/kubevirt-operator.yaml" >/dev/null
kubectl apply -f "$base/kubevirt-cr.yaml" >/dev/null
kubectl -n kubevirt patch kv kubevirt --type merge -p '{"spec":{"configuration":{"developerConfiguration":{"useEmulation":true}}}}' >/dev/null
wait_for 900 "KubeVirt Available" kubectl -n kubevirt wait kv kubevirt --for=condition=Available --timeout=10s

log "PrometheusRule CRD $PROM_OPERATOR_VERSION + fakeprom"
kubectl apply --server-side -f "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/${PROM_OPERATOR_VERSION}/example/prometheus-operator-crd/monitoring.coreos.com_prometheusrules.yaml" >/dev/null
kubectl apply -f "$ROOT/test/e2e/manifests/fakeprom.yaml" >/dev/null
kubectl rollout status deploy/fakeprom --timeout=120s >/dev/null

log "chart: controller on, FullyAutomated, 20s cycles"
helm upgrade --install kubevirt-drs "$ROOT/charts/kubevirt-drs" -n kubevirt-drs --create-namespace \
  --set controller.enabled=true --set image.tag=e2e --set image.pullPolicy=IfNotPresent \
  --set prometheus.url=http://fakeprom.default.svc:9090 \
  --set policy.automationLevel=FullyAutomated --set policy.cycleInterval=20s --set policy.softTainter=true \
  --wait --timeout 5m >/dev/null
wait_for 180 "DRSPolicy Ready=True" bash -c "[[ \$(kubectl get drspolicy default -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}') == True ]]"

log "VMI vm1 (cirros)"
sed "s/__KUBEVIRT_VERSION__/${KUBEVIRT_VERSION}/" "$ROOT/test/e2e/manifests/vmi.yaml" | kubectl apply -f - >/dev/null
wait_for 600 "vm1 Running" bash -c "[[ \$(kubectl get vmi vm1 -o jsonpath='{.status.phase}') == Running ]]"
wait_for 300 "vm1 LiveMigratable" bash -c "[[ \$(kubectl get vmi vm1 -o jsonpath='{.status.conditions[?(@.type==\"LiveMigratable\")].status}') == True ]]"
SRC="$(kubectl get vmi vm1 -o jsonpath='{.status.nodeName}')"
UID_BEFORE="$(kubectl get vmi vm1 -o jsonpath='{.metadata.uid}')"
TARGET=""; for w in "${WORKERS[@]}"; do [[ "$w" != "$SRC" ]] && TARGET="$w" && break; done
echo "    vm1 on $SRC, expected target $TARGET"

log "scenario: vm1 suffers on $SRC (score 20, cpu_ready 25%)"
set_scenario "{\"vmis\":[{\"namespace\":\"default\",\"name\":\"vm1\",\"node\":\"$SRC\",\"score\":20,\"cpuReady\":0.25,\"cpuDemand\":0.5,\"vcpus\":1}],\"nodes\":{\"$SRC\":{\"vmDemand\":0.5,\"score\":20},\"$TARGET\":{\"vmDemand\":0}}}"
wait_for 240 "a recommendation for vm1 exists" bash -c "kubectl get drsrec -n default -l drs.zhanwei.ai/vmi=vm1 --no-headers 2>/dev/null | grep -q ."
REC="$(kubectl get drsrec -n default -l drs.zhanwei.ai/vmi=vm1 -o jsonpath='{.items[0].metadata.name}')"
kubectl get drsrec -n default "$REC" -o custom-columns='NAME:.metadata.name,FROM:.spec.sourceNode,TO:.spec.targetNode,SCORE:.spec.currentScore,EXPECTED:.spec.expectedScore,APPROVED:.spec.approved,PHASE:.status.phase,REASON:.spec.reason'
[[ "$(kubectl get drsrec -n default "$REC" -o jsonpath='{.spec.targetNode}')" == "$TARGET" ]] || die "recommendation targets the wrong node"
[[ "$(kubectl get drsrec -n default "$REC" -o jsonpath='{.spec.approved}')" == true ]] || die "FullyAutomated must pre-approve"
wait_for 60 "recommendation Applied" bash -c "[[ \$(kubectl get drsrec -n default $REC -o jsonpath='{.status.phase}') == Applied ]]"
MIG="$(kubectl get drsrec -n default "$REC" -o jsonpath='{.status.migration}')"
[[ "$(kubectl get vmim -n default "$MIG" -o jsonpath='{.spec.addedNodeSelector.kubernetes\.io/hostname}')" == "$TARGET" ]] || die "migration is not pinned to $TARGET"
[[ "$(kubectl get drspolicy default -o jsonpath='{.status.hotNodes}')" == *"$SRC"* ]] || die "soft tainter did not mark $SRC hot"

log "migration $MIG"
wait_for 600 "migration Succeeded" bash -c "[[ \$(kubectl get vmim -n default $MIG -o jsonpath='{.status.phase}') == Succeeded ]]"
[[ "$(kubectl get vmi vm1 -o jsonpath='{.status.nodeName}')" == "$TARGET" ]] || die "vm1 is not on $TARGET"
[[ "$(kubectl get vmi vm1 -o jsonpath='{.metadata.uid}')" == "$UID_BEFORE" ]] || die "vm1 UID changed (it was restarted, not migrated)"
wait_for 60 "recommendation reports MigrationSucceeded" bash -c "[[ \$(kubectl get drsrec -n default $REC -o jsonpath='{.status.conditions[?(@.type==\"MigrationSucceeded\")].status}') == True ]]"
kubectl get events -n default --field-selector reason=DRSMigration -o custom-columns='REASON:.reason,OBJ:.involvedObject.name,MSG:.message' | tail -2

log "scenario: healthy again -> nothing else moves, taint off"
set_scenario "{\"vmis\":[{\"namespace\":\"default\",\"name\":\"vm1\",\"node\":\"$TARGET\",\"score\":99,\"cpuReady\":0.001,\"cpuDemand\":0.5,\"vcpus\":1}],\"nodes\":{\"$SRC\":{\"vmDemand\":0},\"$TARGET\":{\"vmDemand\":0.5,\"score\":99}}}"
sleep 70
n="$(kubectl get drsrec -A --no-headers | grep -c Pending || true)"
[[ "$n" == 0 ]] || die "$n Pending recommendation(s) on a healthy cluster"
[[ -z "$(kubectl get drspolicy default -o jsonpath='{.status.hotNodes}')" ]] || die "hot taint still set on a healthy cluster"
[[ "$(kubectl get drspolicy default -o jsonpath='{.status.conditions[?(@.type=="ExternalEvacuationsDetected")].status}')" == False ]] || die "ExternalEvacuationsDetected should be False"
kubectl get drspolicy default

# ---- scenario 2: Manual mode. A fresh VMI (vm1 is on cooldown after its move) so the planner may propose it.
# vm2 goes next to vm1 on $TARGET: the node a move just left ($SRC) is not a source again until the smoothing
# window (5m) has passed, so a vm2 scheduled there would wait that long for its recommendation. $SRC is cordoned
# only while vm2 schedules (a cordon is not a rule violation and the taint goes before the scenario starts).
log "Manual mode: VMI vm2 (on $TARGET next to vm1; $SRC cordoned while it schedules)"
kubectl cordon "$SRC" >/dev/null
sed -e "s/__KUBEVIRT_VERSION__/${KUBEVIRT_VERSION}/" -e "s/name: vm1/name: vm2/" "$ROOT/test/e2e/manifests/vmi.yaml" | kubectl apply -f - >/dev/null
wait_for 600 "vm2 Running" bash -c "[[ \$(kubectl get vmi vm2 -o jsonpath='{.status.phase}') == Running ]]"
kubectl uncordon "$SRC" >/dev/null
wait_for 300 "vm2 LiveMigratable" bash -c "[[ \$(kubectl get vmi vm2 -o jsonpath='{.status.conditions[?(@.type==\"LiveMigratable\")].status}') == True ]]"
SRC2="$(kubectl get vmi vm2 -o jsonpath='{.status.nodeName}')"
[[ "$SRC2" == "$TARGET" ]] || die "vm2 landed on $SRC2, expected $TARGET (cordon did not hold?)"
UID2="$(kubectl get vmi vm2 -o jsonpath='{.metadata.uid}')"
TARGET2="$SRC"
# vm2's demand is 45 % of the target's cores: enough to fit once (headroom 55 %) but not twice. A stale check
# that counted the recommendation's own reservation on top of the VM (fixed after the 2026-10-27 lab run)
# would expire it one cycle later and fail the "still Pending" check below.
D2="$(kubectl get node "$TARGET2" -o jsonpath='{.status.allocatable.cpu}' | python3 -c 'import sys
s=sys.stdin.read().strip(); a=float(s[:-1])/1000 if s.endswith("m") else float(s); print(round(0.45*a, 2))')"
# vm1 (healthy, 0.5 cores) shares $SRC2 with vm2; $TARGET2 is empty.
d_src="$(python3 -c "print(round(0.5 + $D2, 2))")"; d_tgt=0
echo "    vm2 on $SRC2 (demand $D2 cores), expected target $TARGET2"
kubectl patch drspolicy default --type merge -p '{"spec":{"automationLevel":"Manual"}}' >/dev/null
set_scenario "{\"vmis\":[{\"namespace\":\"default\",\"name\":\"vm1\",\"node\":\"$TARGET\",\"score\":99,\"cpuReady\":0.001,\"cpuDemand\":0.5,\"vcpus\":1},{\"namespace\":\"default\",\"name\":\"vm2\",\"node\":\"$SRC2\",\"score\":20,\"cpuReady\":0.25,\"cpuDemand\":$D2,\"vcpus\":1}],\"nodes\":{\"$SRC2\":{\"vmDemand\":$d_src,\"score\":20},\"$TARGET2\":{\"vmDemand\":$d_tgt,\"score\":99}}}"
wait_for 300 "a recommendation for vm2 exists" bash -c "kubectl get drsrec -n default -l drs.zhanwei.ai/vmi=vm2 --no-headers 2>/dev/null | grep -q ."
REC2="$(kubectl get drsrec -n default -l drs.zhanwei.ai/vmi=vm2 -o jsonpath='{.items[0].metadata.name}')"
kubectl get drsrec -n default "$REC2" -o custom-columns='NAME:.metadata.name,FROM:.spec.sourceNode,TO:.spec.targetNode,SCORE:.spec.currentScore,EXPECTED:.spec.expectedScore,APPROVED:.spec.approved,PHASE:.status.phase'
[[ "$(kubectl get drsrec -n default "$REC2" -o jsonpath='{.spec.targetNode}')" == "$TARGET2" ]] || die "vm2 recommendation targets the wrong node"
[[ "$(kubectl get drsrec -n default "$REC2" -o jsonpath='{.spec.approved}')" == false ]] || die "Manual must not pre-approve"
log "Manual: unapproved for two cycles -> still Pending, no migration"
sleep 45
[[ "$(kubectl get drsrec -n default "$REC2" -o jsonpath='{.status.phase}')" == Pending ]] || die "unapproved recommendation left Pending: $(kubectl get drsrec -n default "$REC2" -o jsonpath='{.status.phase}')"
[[ -z "$(kubectl get vmim -n default -l "drs.zhanwei.ai/recommendation=$REC2" -o name)" ]] || die "a migration was created without approval"
[[ "$(kubectl get drsrec -n default -l drs.zhanwei.ai/vmi=vm2 --no-headers | wc -l)" == 1 ]] || die "vm2 was recommended more than once while one recommendation was pending"
log "Manual: approve $REC2"
kubectl patch drsrec -n default "$REC2" --type merge -p '{"spec":{"approved":true}}' >/dev/null
wait_for 60 "recommendation Applied" bash -c "[[ \$(kubectl get drsrec -n default $REC2 -o jsonpath='{.status.phase}') == Applied ]]"
MIG2="$(kubectl get drsrec -n default "$REC2" -o jsonpath='{.status.migration}')"
[[ "$(kubectl get vmim -n default "$MIG2" -o jsonpath='{.spec.addedNodeSelector.kubernetes\.io/hostname}')" == "$TARGET2" ]] || die "migration is not pinned to $TARGET2"
wait_for 600 "migration $MIG2 Succeeded" bash -c "[[ \$(kubectl get vmim -n default $MIG2 -o jsonpath='{.status.phase}') == Succeeded ]]"
[[ "$(kubectl get vmi vm2 -o jsonpath='{.status.nodeName}')" == "$TARGET2" ]] || die "vm2 is not on $TARGET2"
[[ "$(kubectl get vmi vm2 -o jsonpath='{.metadata.uid}')" == "$UID2" ]] || die "vm2 UID changed (it was restarted, not migrated)"
set_scenario "{\"vmis\":[{\"namespace\":\"default\",\"name\":\"vm1\",\"node\":\"$TARGET\",\"score\":99,\"cpuReady\":0.001,\"cpuDemand\":0.5,\"vcpus\":1},{\"namespace\":\"default\",\"name\":\"vm2\",\"node\":\"$TARGET2\",\"score\":99,\"cpuReady\":0.001,\"cpuDemand\":0.5,\"vcpus\":1}],\"nodes\":{\"$SRC2\":{\"vmDemand\":0},\"$TARGET2\":{\"vmDemand\":1.0,\"score\":99}}}"

# ---- scenario 3: watchdog. A hand-made approved recommendation pinned to a node that cannot exist.
log "watchdog: grace period 30s, bogus target for vm1 (on $SRC2)"
kubectl patch drspolicy default --type merge -p '{"spec":{"watchdog":{"gracePeriod":"30s","blacklistFor":"1h"}}}' >/dev/null
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: drs.zhanwei.ai/v1alpha1
kind: DRSRecommendation
metadata:
  name: watchdog-bogus-target
  namespace: default
  labels: {drs.zhanwei.ai/vmi: vm1}
spec:
  vmi: vm1
  sourceNode: $SRC2
  targetNode: no-such-node
  currentScore: 50
  expectedScore: 100
  reason: "e2e: a pinned target that cannot exist"
  priority: 2
  approved: true
EOF
wait_for 60 "bogus recommendation Applied" bash -c "[[ \$(kubectl get drsrec -n default watchdog-bogus-target -o jsonpath='{.status.phase}') == Applied ]]"
MIG3="$(kubectl get drsrec -n default watchdog-bogus-target -o jsonpath='{.status.migration}')"
[[ "$(kubectl get vmim -n default "$MIG3" -o jsonpath='{.spec.addedNodeSelector.kubernetes\.io/hostname}')" == no-such-node ]] || die "bogus migration is not pinned to no-such-node"
wait_for 180 "stuck migration $MIG3 deleted by the watchdog" bash -c "! kubectl get vmim -n default $MIG3 >/dev/null 2>&1"
[[ "$(kubectl get drsrec -n default watchdog-bogus-target -o jsonpath='{.status.phase}')" == Failed ]] || die "recommendation should be Failed after the watchdog: $(kubectl get drsrec -n default watchdog-bogus-target -o jsonpath='{.status.phase}')"
kubectl get drsrec -n default watchdog-bogus-target -o jsonpath='    message: {.status.message}{"\n"}'
[[ "$(kubectl get drspolicy default -o jsonpath='{.status.blacklistedPairs[*]}')" == *"$SRC2->no-such-node"* ]] || die "pair $SRC2->no-such-node not blacklisted: $(kubectl get drspolicy default -o jsonpath='{.status.blacklistedPairs[*]}')"
kubectl get events -n default --field-selector reason=DRSMigrationStalled -o custom-columns='TYPE:.type,OBJ:.involvedObject.name,MSG:.message' | tail -2
[[ -n "$(kubectl get events -n default --field-selector reason=DRSMigrationStalled,type=Warning -o name)" ]] || die "no Warning Event DRSMigrationStalled"
[[ "$(kubectl get vmi vm1 -o jsonpath='{.status.nodeName}')" == "$SRC2" ]] || die "vm1 moved during the watchdog test"
log "watchdog: the blacklist survives a controller restart"
CYCLE_BEFORE="$(kubectl get drspolicy default -o jsonpath='{.status.lastCycleTime}')"
kubectl -n kubevirt-drs rollout restart deploy/kubevirt-drs >/dev/null
kubectl -n kubevirt-drs rollout status deploy/kubevirt-drs --timeout=120s >/dev/null
wait_for 120 "a cycle ran after the restart" bash -c "[[ \$(kubectl get drspolicy default -o jsonpath='{.status.lastCycleTime}') != '$CYCLE_BEFORE' ]]"
sleep 25   # one more cycle: the restored blacklist must still be written back
[[ "$(kubectl get drspolicy default -o jsonpath='{.status.blacklistedPairs[*]}')" == *"$SRC2->no-such-node"* ]] || die "blacklist lost after the controller restart"
kubectl get drspolicy default -o jsonpath='    blacklist: {.status.blacklistedPairs[*]}{"\n"}'
log "e2e passed"
