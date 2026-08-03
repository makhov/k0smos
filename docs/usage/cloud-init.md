# Machine configuration

k0smos machines are configured before k0s starts. There is no shell, SSH, or
configuration agent to modify a running node.

Configuration arrives on either:

- a NoCloud ISO labelled `cidata`; or
- an OpenStack config-drive labelled `config-2`.

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
