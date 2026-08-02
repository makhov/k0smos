# Script environment variables

| Variable | Used by | Meaning |
|---|---|---|
| `ARCH` | all | Target arch (`aarch64`/`x86_64`); defaults to the host |
| `IMG` | run-qemu | ext4 disk to switch onto; omit to stay on the initramfs |
| `INITRAMFS`, `KERNEL` | run-qemu | artifact paths |
| `MEM`, `CPUS` | run-qemu | guest sizing |
| `SERIAL` | run-qemu | `stdio` (default) or a file path for headless runs |
| `CONTROL` | run-qemu | control socket path (default `dist/control.sock`) |
| `MONITOR` | run-qemu | optional QEMU monitor socket |
| `CIDATA` | run-qemu | cloud-init drive (ISO) to attach |
| `DATA`, `DATA_SIZE` | run-qemu | data volume to attach; created blank at `DATA_SIZE` (default `4G`) if absent |
| `NET_ARGS` | run-qemu | replaces the default `k0smos.ip=…` cmdline fragment |
| `API_PORT` | run-qemu | host port forwarded to the guest's 6443; unset forwards nothing |
| `CLUSTER_NET`, `CLUSTER_MAC` | run-qemu | second NIC on a shared Ethernet segment: `host:port` of a hub (`internal/nethub`) and this guest's address on it. How several guests on one host reach each other |
| `ROOT` | run-qemu | overrides `k0smos.root=` (default `LABEL=k0smos`) |
| `EXEC` | run-qemu | sets `k0smos.exec=` |
| `K0S_BIN` | mkrootfs, mkinitramfs | k0s binary to bake in |
| `K0S_VERSION` | fetch-k0s | release tag; defaults to latest |
| `FSLABEL`, `APK_PKGS` | mkrootfs | filesystem label (default `k0smos`), extra userspace packages |
| `MODULES_DIR` | mkrootfs, mkinitramfs | module tree to bundle |
| `PUSH`, `REGISTRY`, `TAG` | mkoci | push the OCI image, and where to |
| `MARKER`, `TIMEOUT`, `LOG` | acceptance | readiness pattern, deadline, log path |
| `K0SMOS_E2E_KEEP_CONSOLE` | e2e | `1` keeps guest consoles for passing tests too |
