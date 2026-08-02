# Reach the cluster from the host

QEMU forwards 6443, and `k0smosctl` asks the running node for its
kubeconfig over the control port — no shell on the guest, no shutting it down
first:

```bash
k0smosctl cluster kubeconfig -o kubeconfig
KUBECONFIG=kubeconfig kubectl get nodes
```

The server address is rewritten from `localhost` (right on the node, wrong
everywhere else) to `127.0.0.1` and the guest's own forwarded port, which `machine up`
recorded — so a second guest's kubeconfig points at the second guest. `--server
host:port` overrides it, `--server ''` keeps what the node wrote, and `-o -` prints
instead of writing a file. The file is written 0600, because it is a cluster-admin
credential.

If the cluster has not written its PKI yet you get a clear error naming
`admin.conf` rather than an empty file.

> Whoever can write to the control port obtains cluster-admin. That is not a new
> exposure — the same channel stops the machine — but do not expose the port
> anywhere the disk is not equally exposed.

Reading it off the disk still works and needs no running guest, which is
occasionally what you want:

```bash
k0smosctl machine shutdown
docker run --rm -v "$PWD/dist:/d" alpine:3.20 sh -c \
  'apk add -q --no-cache e2fsprogs e2fsprogs-extra >/dev/null &&
   debugfs -R "cat /var/lib/k0s/pki/admin.conf" /d/k0smos.img 2>/dev/null' \
  | sed 's/localhost/127.0.0.1/' > kubeconfig
```

Docker because `debugfs` is not on macOS. If the data volume is separate, read
`/var/lib/k0s` from that image instead — the root image will only have the empty
mountpoint.
