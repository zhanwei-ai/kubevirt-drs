#!/usr/bin/env bash
# End-to-end test on kind: KubeVirt in software emulation, a scripted stand-in for Prometheus, the chart with
# the controller in FullyAutomated mode. Proves the wiring: Prometheus says VM suffers -> recommendation ->
# pinned migration -> the VM runs on the other node with the same UID -> healthy again, nothing more happens.
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
log "e2e passed"
