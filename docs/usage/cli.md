# The CLI

`k0smosctl` runs on the host and covers the routine work. Build it once:

```bash
make ctl        # -> dist/k0smosctl
```

| Command | What it does |
|---|---|
| `k0smosctl gen` | write a cloud-init drive for a node to boot with |
| `k0smosctl machine up` | create or start one local artifact-backed machine |
| `k0smosctl machine logs` | show a machine's console (`-f` to follow) |
| `k0smosctl machine list` | what machines exist, and which are running |
| `k0smosctl machine shutdown` / `reboot` | stop or restart a machine cleanly |
| `k0smosctl machine rm` | discard a stopped machine and its disk |
| `k0smosctl cluster create` | create a Ready local cluster from one artifact |
| `k0smosctl cluster rm` | cleanly stop and discard a local cluster and its network |
| `k0smosctl cluster kubeconfig` | fetch the admin kubeconfig from a running cluster |
| `k0smosctl cluster token` | mint a join token so another machine can join |

Each takes `--help`. Two things it deliberately replaces: building an ISO with
`xorriso`, and reading a kubeconfig off a stopped guest's disk with `debugfs` —
neither of which is installed on macOS, so both meant Docker.

A node has no SSH and no shell. It answers a small set of requests on a
virtio-serial control port instead:

```bash
k0smosctl cluster kubeconfig -o kubeconfig   # then KUBECONFIG=kubeconfig kubectl get nodes
k0smosctl cluster token --role controller    # a join token, so another machine can join
k0smosctl machine shutdown                   # or reboot — never kill QEMU
```

The kubeconfig comes off the node's filesystem, so it works whether or not k0s is
still running, and says so plainly when the cluster has not written its PKI yet.
The API server address is rewritten to `127.0.0.1`, where the local QEMU machine
forwards 6443.

A join token is signed with the cluster CA, so only a machine already in the
cluster can produce one — the node runs `k0s token create` and sends the result
back. That is what lets a machine with no shell be joined to; see
[multi-node.md](multi-node.md) for the whole flow.

Whoever can write to that port obtains cluster-admin, and a controller token
confers control-plane membership. That is not a new exposure — the same channel
stops the machine — but do not expose the port anywhere the disk is not equally
exposed.
