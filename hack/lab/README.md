# Lab material

`stress-vm.yaml`: namespace `drs-lab` with three Fedora cloud VMs (containerdisk, 2 vCPU, 640Mi) that burn
every vCPU from first boot with a plain shell loop (cloud-init `runcmd`; no package install, no SSH, no key).
Console login `fedora` / `kubevirt-drs` if you need to look inside (`virtctl console -n drs-lab stress-1`).

Produce CPU contention on one node, then let the brain find it:

```bash
kubectl cordon <node-b> <node-c>                 # everything lands on node-a
kubectl apply -f hack/lab/stress-vm.yaml
kubectl -n drs-lab get vmi -w                    # wait for Running on node-a
kubectl uncordon <node-b> <node-c>               # never use soft affinity: the scheduler would pull them back
# two to three minutes later (rate windows + smoothing):
kubectl get drspolicy default -o yaml | yq '.status'
kubectl get drsrec -A
```

Clean up: `kubectl delete namespace drs-lab`.

Calibration steps: [docs/calibration.md](../../docs/calibration.md).
