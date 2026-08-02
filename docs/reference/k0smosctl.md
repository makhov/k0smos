# k0smosctl

!!! note "This page is generated"
    Every description and flag table below is copied from `k0smosctl <command>
    --help`, not typed by hand, so it cannot drift from the code by transcription
    error. When you add or change a flag, regenerate it:

    ```bash
    go build -o /tmp/k0smosctl ./cmd/k0smosctl
    for c in gen "machine up" "machine logs" "machine list" "machine shutdown" \
             "machine reboot" "machine rm" "cluster create" "cluster kubeconfig" \
             "cluster token" "cluster rm"; do
      echo "=== k0smosctl $c ==="
      /tmp/k0smosctl $c --help
    done
    ```

    then update the affected section(s) of this page by hand from the output.

`k0smosctl` is the host-side CLI that builds cloud-init drives and drives local
machines and clusters. See [The CLI](../usage/cli.md) for the narrative — what it
replaces, how the control port works, and the security note. This page is the
flag reference.

## k0smosctl gen

Writes a NoCloud cloud-init drive: user-data and meta-data at the image root,
with Rock Ridge names so "user-data" survives intact.

The ISO is written directly, so no xorriso — and on macOS no Docker — is needed.
What it generates is parsed before being written, so a mistake surfaces here
rather than as a console warning after the machine has already booted.

| Flag | Description |
|---|---|
| `--file stringArray` | place a host file on the node, as SRC:DEST (repeatable) |
| `-h, --help` | help for gen |
| `--hostname string` | local-hostname for the node |
| `--instance-id string` | instance-id for meta-data (default "k0smos") |
| `--label string` | volume label k0smos looks for (default "cidata") |
| `-o, --output string` | output image path (default "cidata.iso") |
| `--user-data string` | cloud-config file to use as-is, or "-" for stdin |

## k0smosctl machine

Create and operate local artifact-backed machines. Takes no flags of its own
besides `-h, --help`; see the subcommands below.

### k0smosctl machine up

Boots one prepared k0smos platform artifact under QEMU.

By default k0smosctl resolves the latest GitHub release, downloads its matching
firmware-bootable qcow2 into the local cache, verifies the published checksum,
and reuses it on later runs. `--release` pins a tag; `--image` selects a local
file and bypasses downloading. The complete artifact is cloned into the guest's
state directory and QEMU boots its own UEFI, GRUB, kernel, initramfs, immutable
EROFS root and writable `/var`. No build tree or separate boot inputs are needed.

`--direct-kernel` keeps the development path used by low-level tests. In that mode
QEMU receives `--kernel` and `--initramfs` directly; `--disk`/`--from-disk` select
the legacy ext4 root. Flags specific to that path imply `--direct-kernel` for
backward compatibility.

The guest runs in the background and the command returns: there is no shell on a
k0smos node, so there is nothing to sit in front of. Its console goes to a file
readable with `k0smosctl machine logs`, port 6443 is forwarded, and a control
port is attached so `k0smosctl cluster kubeconfig` and `k0smosctl machine
shutdown` can reach it.

Each guest is identified by `--name` and keeps its console, control socket and
machine disk under its own state directory. The resolved artifact is cloned the
first time, so the cached image stays a pristine template and a second guest
needs only a name and its own `--api-port`. Later boots of the same name reuse
its disk, keeping the cluster.

Use `--attach` to stay and watch the console instead, where ctrl-c then shuts the
guest down cleanly rather than killing it.

| Flag | Description |
|---|---|
| `--api-port int` | host port forwarded to the API server; 0 forwards nothing (default 6443) |
| `--arch string` | guest architecture: amd64 or arm64 (default "arm64") |
| `--attach` | stay in the foreground streaming the console; ctrl-c then shuts the guest down cleanly |
| `--cache-dir string` | release artifact cache (default ~/.cache/k0smos/images) |
| `--cidata string` | cloud-init drive to attach, as written by 'k0smosctl gen' |
| `--console string` | where to write the console (default: under the guest's state directory) |
| `--cpus string` | guest CPUs (default "2") |
| `--data string` | data volume for /var (direct-kernel mode; platform artifacts include one) |
| `--data-size string` | size for a newly created --data volume (default "4G") |
| `--direct-kernel` | use separate kernel/initramfs development inputs instead of the platform artifact |
| `--disk string` | use this raw ext4 root directly (direct-kernel development mode) |
| `--dry-run` | print the qemu command instead of running it |
| `--exec string` | override the supervised workload in direct-kernel mode (comma-separated) |
| `--firmware string` | UEFI code image (auto-detected from QEMU when omitted) |
| `--from-disk` | clone --image as a legacy ext4 root (direct-kernel development mode) |
| `-h, --help` | help for up |
| `--image string` | firmware-bootable qcow2; bypasses GitHub release resolution |
| `--initramfs string` | initramfs image (direct-kernel development mode) (default "dist/k0smos-initramfs.gz") |
| `--interactive` | hand QEMU the terminal (implies --attach; escape is ctrl-a x). The guest has no shell, so this is rarely useful |
| `--kernel string` | kernel image (direct-kernel development mode) |
| `--memory string` | guest memory in MiB (default "4096") |
| `--name string` | name for this guest; its console and control socket are kept under it (default "default") |
| `--no-image` | boot only the initramfs in direct-kernel smoke mode (kubelet cannot run there) |
| `--release string` | k0s-tagged GitHub release to use when --image is omitted: latest or vX.Y.Z+k0s.N (default "latest") |
| `--socket string` | control socket path (default: under the guest's state directory) |

### k0smosctl machine logs

Prints the console of a guest started by `k0smosctl machine up`.

The console is the only thing a k0smos node reports through — there is no shell
and no SSH — so this is how you watch a boot, and where k0s's own logs appear.

| Flag | Description |
|---|---|
| `-f, --follow` | keep printing as the guest writes more |
| `-h, --help` | help for logs |
| `-n, --lines int` | start from the last N lines instead of the beginning |
| `--name string` | which guest (default "default") |

### k0smosctl machine list

Lists the guests `k0smosctl machine up` has started, and whether each is still
up.

Liveness comes from its control socket answering, not from the recorded pid: a
pid can be reused, and a socket that answers is the thing the other subcommands
actually need.

Aliases: `list`, `ls`

| Flag | Description |
|---|---|
| `-h, --help` | help for list |

### k0smosctl machine shutdown

Shut a running machine down cleanly.

Use this rather than killing QEMU: a hard kill leaves the ext4 root with an
unreplayed journal, which loses recent writes and makes the image read as empty
afterwards.

| Flag | Description |
|---|---|
| `-h, --help` | help for shutdown |
| `--name string` | which guest (default "default") |
| `--socket string` | control socket path, instead of resolving --name |
| `--timeout duration` | how long to wait for the socket (default 5s) |

### k0smosctl machine reboot

Restart a running machine cleanly.

Use this rather than killing QEMU: a hard kill leaves the ext4 root with an
unreplayed journal, which loses recent writes and makes the image read as empty
afterwards.

| Flag | Description |
|---|---|
| `-h, --help` | help for reboot |
| `--name string` | which guest (default "default") |
| `--socket string` | control socket path, instead of resolving --name |
| `--timeout duration` | how long to wait for the socket (default 5s) |

### k0smosctl machine rm

Deletes a guest's state directory: its root disk, console and metadata.

The next `machine up` with that name starts again from a fresh clone of the
image, which is how a k0smos node is meant to be treated — replaced rather than
repaired.

Refuses while the guest is still running, since removing a disk from under QEMU
corrupts it.

| Flag | Description |
|---|---|
| `-h, --help` | help for rm |
| `--name string` | which guest (default "default") |

## k0smosctl cluster

Create and access Kubernetes clusters on k0smos machines. Takes no flags of its
own besides `-h, --help`; see the subcommands below.

### k0smosctl cluster create

Creates a local k0s cluster from one firmware-bootable k0smos qcow2.

When `--image` is omitted, the matching qcow2 is downloaded from the requested
GitHub release, checksum-verified, and reused from the local cache. Every
machine receives its own copy-on-write clone and config drive, but boots that
same immutable artifact. A rootless userspace Ethernet segment connects the
machines; the first controller bootstraps k0s, and join tokens minted by that
controller are placed on the remaining machines' config drives.

Controllers also run workloads, so the default one-controller cluster is useful
without a worker. The command waits for the API and writes an immediately usable
kubeconfig before returning.

| Flag | Description |
|---|---|
| `--api-port int` | host port for the first controller; later controllers use consecutive ports (default 6443) |
| `--arch string` | guest architecture: amd64 or arm64 (default "arm64") |
| `--cache-dir string` | release artifact cache (default ~/.cache/k0smos/images) |
| `--controllers int` | number of controller machines (default 1) |
| `--cpus int` | CPUs per machine (default 2) |
| `--dry-run` | print the machine plan without starting anything |
| `--firmware string` | UEFI code image (auto-detected from QEMU when omitted) |
| `-h, --help` | help for create |
| `--image string` | firmware-bootable qcow2; bypasses GitHub release resolution |
| `--memory int` | memory per machine in MiB (default 4096) |
| `--name string` | cluster name; machine names are derived from it (default "dev") |
| `-o, --output string` | where to write the admin kubeconfig (default "kubeconfig") |
| `--release string` | k0s-tagged GitHub release to use when --image is omitted: latest or vX.Y.Z+k0s.N (default "latest") |
| `--timeout duration` | time allowed for the cluster to become ready (default 10m0s) |
| `--workers int` | number of worker-only machines |

### k0smosctl cluster kubeconfig

Asks a running node for its admin kubeconfig over the control port.

This replaces reading the guest's disk offline with debugfs, which meant
shutting the machine down first and, on macOS, a Docker container to supply
e2fsprogs.

The node reads the file off its filesystem, so this works whether or not k0s is
still running, and says so plainly when the cluster has not created it yet.

Note that whoever can write to the control port obtains cluster-admin. That is
not a new exposure — the same channel can stop the machine — but the port should
not be exposed anywhere the disk is not.

| Flag | Description |
|---|---|
| `-h, --help` | help for kubeconfig |
| `--name string` | which guest (default "default") |
| `-o, --output string` | where to write it, or "-" for stdout (default "kubeconfig") |
| `--server string` | rewrite the API server as host or host:port; "" keeps what the node wrote (default "127.0.0.1") |
| `--socket string` | control socket path, instead of resolving --name |
| `--timeout duration` | how long to wait for the node to answer (default 10s) |

### k0smosctl cluster token

Asks a running node to create a k0s join token.

A join token is signed with the cluster CA, so only a machine already in the
cluster can produce one — which is why this goes to the node rather than being
computed here. Hand the result to the joining machine as a file, and point k0s
at it with `--token-file`.

Minting one waits on the API server, so a node that has only just started may
take a while to answer; raise `--timeout` rather than retrying.

The same caution as kubeconfig applies: a controller token confers control-plane
membership on whoever holds it.

| Flag | Description |
|---|---|
| `-h, --help` | help for token |
| `--name string` | which guest (default "default") |
| `-o, --output string` | where to write it, or "-" for stdout (default "join-token") |
| `--role string` | what the joining machine will be: controller or worker (default "worker") |
| `--socket string` | control socket path, instead of resolving --name |
| `--timeout duration` | how long to wait for the node to answer (default 2m0s) |

### k0smosctl cluster rm

Shuts every machine down cleanly, stops the cluster's userspace network, then
removes the machine clones, config drives and recorded cluster state.

It refuses to remove disks if a machine does not shut down within the timeout.

Aliases: `rm`, `delete`

| Flag | Description |
|---|---|
| `-h, --help` | help for rm |
| `--name string` | cluster to remove (default "dev") |
| `--timeout duration` | time allowed for clean machine shutdown (default 2m0s) |
