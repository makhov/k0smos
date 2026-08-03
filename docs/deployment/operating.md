# Operating machines

k0smos follows an appliance lifecycle: configure, boot, observe, replace.

## Observe through the console

There is no SSH daemon or shell. Every boot stage and the supervised k0s process
write to the machine console.

- local QEMU: `k0smosctl machine logs -f`
- KubeVirt: VMI serial console and Kubernetes events
- bare metal: serial-over-LAN or the BMC console

Container workload logs remain Kubernetes logs and should be collected through
the cluster logging stack.

## Persist `/var`

The system partition is read-only. All mutable state is under `/var`, including
k0s, etcd, containerd, kubelet state, and pulled images.

The metal qcow2 contains an ext4 `k0smos-data` partition mounted at `/var`.
KubeVirt requires a separate data disk selected with `k0smos.data=auto`.

Choose its lifecycle deliberately:

| Storage | Result |
|---|---|
| ephemeral VM disk | survives reboot; removed with the machine |
| PVC or persistent disk | can outlive the machine |
| metal image data partition | remains on the installed disk |

Cluster API machines should still be treated as replaceable even when their
storage persists.

## Shut down cleanly

Use the platform's normal graceful stop operation. Locally:

```bash
k0smosctl machine shutdown --name node-1
```

k0smos also listens for an ACPI power-button event. During shutdown it asks
multi-controller k0s nodes to leave etcd, stops processes, syncs and unmounts
filesystems, and remounts the system read-only before powering off.

Do not hard-kill the hypervisor except as a last resort; recent `/var` writes may
be lost.

## Replace instead of upgrading in place

k0smos currently has no in-place or A/B update mechanism. To move to another
k0s release, deploy the new release artifact and replace machines using the
platform's rollout mechanism.

For local development, `machine rm` discards a stopped machine and the next
`machine up` creates a fresh clone.

## Retrieve cluster access

For local machines, the control channel can return the admin kubeconfig:

```bash
k0smosctl cluster kubeconfig --name node-1 -o kubeconfig
```

This local control channel is not currently attached by the KubeVirt manifests.
On managed deployments, obtain cluster access through the management cluster or
the Kubernetes API endpoint rather than relying on `k0smosctl`.
