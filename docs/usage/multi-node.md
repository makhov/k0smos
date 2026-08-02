# More than one node

For local QEMU, prefer `k0smosctl cluster create`; it automates everything in
this section. The details below describe the provider-facing mechanism and are
useful when another system creates the machines.

A second machine joins with a token, which only a machine already in the cluster
can produce — it is signed with the cluster CA. `k0smosctl cluster token` asks a running
node for one over the control port, the same way `kubeconfig` does:

```bash
k0smosctl cluster token --role controller -o join-token   # or --role worker
```

The joining node needs three things: the token, its own address, and a k0s
configuration naming that address. Nodes have to reach each other, so give each
one an address on a network they share and tell kubelet to use it — left alone,
kubelet picks the address behind the default route and every node claims the same
one.

```yaml
# node2.yaml
#cloud-config
write_files:
  - path: /etc/k0s/k0s.yaml
    content: |
      apiVersion: k0s.k0sproject.io/v1beta1
      kind: ClusterConfig
      spec:
        api:
          address: 10.10.0.12
          sans: [10.10.0.11, 10.10.0.12, 127.0.0.1]
        storage:
          type: etcd
          etcd:
            peerAddress: 10.10.0.12
```

```bash
k0smosctl gen --user-data node2.yaml --hostname node2 \
  --file join-token:/etc/k0s/join-token -o node2.iso
```

`spec.api.address` and `spec.storage.etcd.peerAddress` are per-node: they say
which address this machine answers on, so they cannot come from the shared cluster
configuration. The SAN list has to cover every node's address, plus `127.0.0.1` if
you reach the API through a forwarded port.

The first node bootstraps the cluster and needs no token; the rest are booted with
`--token-file`:

```
k0smos.exec=/usr/local/bin/k0s,controller,--enable-worker,--no-taints,\
--config=/etc/k0s/k0s.yaml,--token-file=/etc/k0s/join-token,\
--kubelet-extra-args=--node-ip=10.10.0.12
```

`--enable-worker --no-taints` makes each node a control plane that also runs
workloads. Leave them off for a worker-only machine and use `--role worker` when
minting its token.

Three of these form an etcd quorum. `e2e/cluster_test.go` builds exactly that, and
is the place to look for a working set of arguments.

> A controller token confers control-plane membership on whoever holds it. Treat
> it like the kubeconfig above.

Locally, guests need a network they share: QEMU's user-mode networking puts every
guest behind its own NAT at the same address, where they cannot see each other.
`run-qemu.sh` takes `CLUSTER_NET=<host:port>` to give a guest a second NIC
connected to an Ethernet hub, and `CLUSTER_MAC=` to keep their addresses distinct
on it. `internal/nethub` is that hub, and the e2e suite runs one in-process; it
has to be listening before a guest starts, because QEMU does not retry. QEMU's own
answers do not work here — tap and vmnet want root, and its multicast backend is
silently dead on macOS. On real hardware or KubeVirt none of this arises: the
machines are already on a network.
