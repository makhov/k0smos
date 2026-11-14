# Operating machines

k0smos follows an appliance lifecycle: configure, boot, observe, replace.

## Observe

There is no SSH daemon or shell. Every boot stage and the supervised k0s process
write to the machine console:

- local QEMU: `k0smosctl machine logs -f`
- KubeVirt: VMI serial console and Kubernetes events
- bare metal: serial-over-LAN or the BMC console

For a local machine you can also ask it directly rather than reading scrollback:

```bash
k0smosctl machine status --name node-1   # what the init decided, and workload restarts
k0smosctl machine dmesg  --name node-1   # kernel ring buffer, k0smos's own lines included
k0smosctl machine cat    --name node-1 /etc/k0s/k0s.yaml
```

These use the control port, which `k0smosctl` attaches locally. On KubeVirt and
bare metal the serial console is the channel; the same boot record is on the
machine at `/run/k0smos/boot.json`.

Everything k0smos prints also goes to the kernel ring buffer, so `dmesg` holds
the whole boot -- init's decisions in order against the kernel's own messages --
after the console has scrolled. The last line init prints is the verdict, which
is what to look for first in a captured console:

```text
k0smos: boot ok in 4.2s, 11 stages
k0smos: boot DEGRADED in 6.1s, 11 stages: network (dhcp on eth0: no lease)
```

### Without `k0smosctl`

The control port is a virtio-serial port and a line protocol, so any tool can
ask. A machine booted by hand needs the port wired up:

```
-chardev socket,path=/tmp/ctl.sock,server=on,wait=off,id=ctl
-device virtio-serial-pci
-device virtserialport,chardev=ctl,name=k0smos.control
```

Then a request is a line, and the reply is `ok <bytes>` followed by that many
bytes (or `err <message>`):

```bash
printf 'status\n' | socat - UNIX-CONNECT=/tmp/ctl.sock
```

The requests are `status`, `dmesg`, `cat <absolute path>`, `kubeconfig` and
`token controller|worker`. Whatever can write to that socket holds
cluster-admin, so it belongs where the machine's disk already is.

### KubeVirt

`virtctl console <vm>` shows a boot as it happens. To keep it, the VM sets
`logSerialConsole: true` under `spec.template.spec.domain.devices`, as the
manifests in this repository do, and the console becomes a pod log:

```bash
kubectl logs -l kubevirt.io/domain=node-1 -c guest-console-log --tail=-1
```

It lives as long as the launcher pod, so copy it before replacing a machine.

### Bare metal, over the network

A machine with no usable BMC can stream its kernel ring buffer -- which holds
k0smos's own lines too -- to a collector over UDP, using the kernel's
netconsole. It is a module, so it is added on the command line along with its
target, `[source-port]@[source-ip]/[dev],[target-port]@<target-ip>/[target-mac]`:

```
k0smos.modules=+netconsole netconsole.netconsole=6666@/,514@10.0.0.5/
```

Collect it with anything that reads syslog, or `socat -u UDP-RECV:514 -`. It is
plaintext UDP with no retransmission, so treat it as a console and not as an
audit log; it also starts only once its NIC driver is loaded, so the first
kernel messages of a boot are not in it.

### From inside the cluster

Once k0s is up, the boot record is reachable on every platform without a
console, because the machine is a node:

```bash
kubectl debug node/node-1 -it --image=busybox -- cat /host/run/k0smos/boot.json
```

See [troubleshooting](../troubleshooting.md) for how to read them.

Container workload logs remain Kubernetes logs and should be collected through
the cluster logging stack.

## Persist `/var`

The system partition is read-only. All mutable state is under `/var`, including
k0s, etcd, containerd, kubelet state, and pulled images.

The metal qcow2 contains an ext4 `k0smos-data` partition mounted at `/var`.
KubeVirt requires a separate data disk selected with `k0smos.data=auto`.

`/var` grows into its disk on boot. Write the image onto a bigger disk, or
resize the volume (`qemu-img resize`, an RBD or cloud volume resize, an
expanded PVC), and the next boot extends the data partition to the end of the
disk and grows the filesystem online:

```text
k0smos: grew partition 3 of vda from 8.0 GiB to 200.0 GiB; grew the filesystem at /var from 8.0 GiB to 200.0 GiB
```

Only the last partition on a GPT disk is extended, and only ever grown; a
filesystem directly on a whole disk is just grown. Nothing happens when there is
no room, so the check costs nothing on later boots. Shrinking is not supported.

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
