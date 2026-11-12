# Machine configuration

k0smos machines are configured before k0s starts. There is no shell, SSH, or
configuration agent to modify a running node.

Configuration arrives on either:

- a NoCloud ISO labelled `cidata`; or
- an OpenStack config-drive labelled `config-2`.

Either label may be upper case (`CIDATA`), as standard-following ISO writers
produce it, and the drive may be a virtio disk or a cdrom on virtio-scsi, as
libvirt usually attaches a seed. A cdrom needs a kernel with a
CD-ROM driver: the metal image's has one; Kata's guest kernel does not.

k0smos reads both formats directly in userspace. This is the same interface used
by local `k0smosctl` machines and by Cluster API providers.

## Generate a local configuration drive

```bash
k0smosctl gen \
  --hostname node-1 \
  --file k0s.yaml:/etc/k0s/k0s.yaml \
  -o node-1.iso

k0smosctl machine up --name node-1 --cidata node-1.iso
```

`gen` writes the ISO without external tooling and validates the configuration
with the same parser used in the guest.

To use an existing cloud-config document:

```bash
k0smosctl gen --user-data cloud-config.yaml --hostname node-1 -o node-1.iso
```

The document must begin with `#cloud-config`.

## Supported cloud-init fields

### `write_files`

Supported fields are `path`, `content`, `permissions`, and `encoding`. Encodings
may be plain, `b64`, `gzip+base64`, or `gz+b64`. Parent directories are created
automatically.

Use `write_files` for:

- `/etc/k0s/k0s.yaml`;
- `/etc/k0s/join-token`;
- registry credentials and trust material; and
- Kubernetes manifests under `/var/lib/k0s/manifests/<stack>/`.

### Hostname and network

`local-hostname` in metadata sets the hostname. An optional `k0smos` section in
cloud-config sets machine networking:

```yaml
#cloud-config
k0smos:
  iface: eth0
  ip: 10.20.0.12/24
  gateway: 10.20.0.1
  dns: 10.20.0.53
```

Values supplied here override the corresponding kernel command-line fields.

### `network-config`

A NoCloud drive's `network-config` file, as `cloud-localds -N` writes it, configures interfaces in netplan's version 2
format, with or without a top-level `network:` key:

```yaml
network:
  version: 2
  ethernets:
    eth0:
      mtu: 9000
      addresses: [10.20.0.12/32]
      routes:
        - {to: default, via: 10.20.0.1}
        - {to: 10.20.0.1/32, scope: link}
      nameservers:
        addresses: [10.20.0.53, 1.1.1.1]
```

Supported per `ethernets` entry: the key as the interface name, or
`match: {macaddress: ...}`; `dhcp4`; one IPv4 address; `gateway4` or a default
route with `via` (and `on-link`); routes with `scope: link`, which make a
gateway outside the address's prefix reachable; `nameservers`; `mtu`. Bonds,
VLANs, bridges, IPv6, other routes and version 1 are reported on the console
and skipped.

It overrides the kernel command line (the metal image's fixed
`k0smos.ip=dhcp`), and the `k0smos` section of user-data overrides it.

### Extra disks: `fs_setup` and `mounts`

The data volume at `/var` holds k0s and its containers. A disk for anything
else (a second NVMe on a bare-metal machine, a disk attached in libvirt or
QEMU) is set up with cloud-init's own keys:

```yaml
#cloud-config
fs_setup:
  - {device: /dev/nvme1n1, filesystem: ext4, label: scratch}
mounts:
  - [LABEL=scratch, /srv/scratch]
```

Format by device once, then mount by label. There is no udev, so there are no
`/dev/disk/by-id` links, and a name such as `/dev/nvme1n1` can change between
boots on real hardware. A label cannot.

`fs_setup` formats a device **only if it is blank**, meaning its first MiB is all
zero, as on a disk that has never been used. A disk holding anything (a
filesystem, a partition table, LVM) is never formatted:

- one with a filesystem k0smos recognises (ext4, XFS, vfat) is left as it is, so
  the entry is harmless on every later boot;
- any other is refused with `refusing to format`. Wipe it first if it is meant
  for this node.

Supported:

| Key | Supported | Not supported (warned and skipped) |
|---|---|---|
| `fs_setup` | `device` (a `/dev` path), `filesystem: ext4` or `xfs`, `label` | other filesystems, `overwrite`, `partition` other than `none`/`auto`/`any`, aliases such as `ephemeral0` |
| `mounts` | device as `/dev/...`, `sdb`, `LABEL=` or `UUID=`; mount point; filesystem type or `auto`; options `defaults`, `nofail`, `rw`, `ro` | swap, other options |
| `disk_setup` | | partitioning: give `fs_setup` a whole disk or an existing partition |

Mount points go under `/srv`, `/mnt` or `/var`. The root filesystem is
read-only, and those are the directories where k0smos can create one.

With `k0smos.data=auto`, as on KubeVirt, a blank disk named in `fs_setup` is
never chosen as the data volume. So a VM can be given a blank data disk and a
blank extra disk on its first boot.

A failed entry is reported on the console and does not stop the boot or the
other entries. A node whose extra disk is missing still comes up.

### `runcmd`

k0smos **interprets** a deliberately small subset; it never invokes a shell.
Supported operations are:

- `mkdir -p`
- `chmod`
- `chown`
- `ln -s`
- `k0s install controller ...`
- `k0s install worker ...`

For `k0s install`, k0smos translates the service-install command into the
foreground process it supervises. `systemctl` entries are ignored because there
is no service manager.

Other commands are logged as `UNSUPPORTED runcmd` and are not executed. Avoid
bootstrap configuration that depends on shell scripts, package installation,
`curl`, pipes, or redirection.

## Kubernetes manifests

k0s reconciles files beneath `/var/lib/k0s/manifests/<stack>/`. Delivering them
with `write_files` applies them on the first reconciliation without running
`kubectl` on the node:

```bash
k0smosctl gen \
  --file namespace.yaml:/var/lib/k0s/manifests/platform/namespace.yaml \
  --file deployment.yaml:/var/lib/k0s/manifests/platform/deployment.yaml \
  -o node-1.iso
```

## Security boundary

Configuration data can contain cluster-admin credentials and controller join
tokens. Protect configuration drives and provider bootstrap Secrets as you
would protect the machine disk itself.
