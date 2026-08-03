# Local machines

Use `machine up` when you need one node rather than a managed local cluster.

```bash
k0smosctl machine up --name node-1
```

The CLI downloads and verifies the latest qcow2 when necessary, creates a
private machine disk, boots it in the background, and forwards the Kubernetes
API to host port 6443.

## Configure the machine

Generate a cloud-init drive and attach it on first boot:

```bash
k0smosctl gen \
  --hostname node-1 \
  --file k0s.yaml:/etc/k0s/k0s.yaml \
  -o node-1.iso

k0smosctl machine up --name node-1 --cidata node-1.iso
```

See [machine configuration](cloud-init.md) for the supported cloud-init subset.

## Operate it

```bash
k0smosctl machine list
k0smosctl machine logs --name node-1 -f
k0smosctl cluster kubeconfig --name node-1 -o kubeconfig
k0smosctl machine reboot --name node-1
k0smosctl machine shutdown --name node-1
```

The guest has no interactive shell. `machine logs` is the normal way to observe
it. Use `--attach` with `machine up` to stream the console in the foreground;
Ctrl-C then requests a clean shutdown.

## Persistence and replacement

A later `machine up --name node-1` reuses that machine's disk, including `/var`
and its k0s state. To replace the machine with a fresh clone:

```bash
k0smosctl machine shutdown --name node-1
k0smosctl machine rm --name node-1
k0smosctl machine up --name node-1
```

`machine rm` refuses to delete a running machine.

## Multiple independent machines

Names isolate disks, consoles, sockets, and metadata. API ports must also be
unique:

```bash
k0smosctl machine up --name node-1 --api-port 6443
k0smosctl machine up --name node-2 --api-port 7443
```

These machines are independent. Use `cluster create` when they should form one
cluster; it also provides a shared guest network and join tokens.

## Local and pinned images

```bash
k0smosctl machine up --release v1.36.3+k0s.0
k0smosctl machine up --image ./k0smos-metal-x86_64.qcow2
```

The `--direct-kernel` flags are a low-level development interface. They are not
needed to consume a platform artifact.
