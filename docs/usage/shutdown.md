# Shut it down

**Do not kill QEMU.** A hard kill leaves the ext4 journal unreplayed and any later
disk inspection will lie to you.

```bash
k0smosctl machine shutdown        # or: k0smosctl machine reboot
./image/poweroff.sh              # the same thing, without building the CLI
```

Both write to a virtio-serial control port. A real hypervisor or machine uses the
ACPI power button instead, which k0smos also watches — the control port exists
because QEMU's arm64 `virt` machine with direct kernel boot has no ACPI at all.
`SIGTERM`/`SIGINT` work too.

On the way down k0smos runs `k0s etcd leave` **before** stopping the child (a
controller cannot give up membership once stopped), then kills everything, syncs,
unmounts deepest-first, and remounts `/` read-only. The evidence that it worked is
that `e2fsck -fn` afterwards is completely silent — no journal to replay.

Skipped for workers and for `--single`, which is kine-backed rather than etcd.
