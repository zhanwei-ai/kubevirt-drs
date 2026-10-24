# Lab material

`stress-vm.yaml`: a Fedora cloud VM (containerdisk, 2 vCPU, 2 GiB) with `stress-ng` and `sysbench`
installed by cloud-init. Create several on one node (cordon the others first), then drive load from inside:

```bash
kubectl apply -f hack/lab/stress-vm.yaml            # edit metadata.name for more copies
virtctl ssh fedora@stress-1 -c 'stress-ng --cpu 2 --timeout 0 &'
virtctl ssh fedora@stress-1 -c 'sysbench cpu --time=30 run | grep "events per second"'
```

Calibration steps: [docs/calibration.md](../../docs/calibration.md). Credentials: user `fedora`, SSH key
injected from `~/.ssh/id_ed25519.pub` by the cloud-init block below (replace it).
