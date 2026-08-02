# Cloud

Feasible but the least exercised path.

- **Addressing** comes from DHCP (`k0smos.ip=dhcp`), which is what every major
  provider expects.
- **Boot** is the obstacle. Providers that allow direct kernel boot (OpenStack,
  some bare-metal clouds) work like the libvirt case. EC2 and similar boot
  through their own chain and would need a bootloader in the image, which is not
  built here.
- **Metadata is not wired up.** There is no IMDS or config-drive support, so
  hostname, SSH keys and user-data are not read. Set the hostname via
  `k0smos.hostname=` on the cmdline instead. Note IMDS could not solve
  addressing anyway — you need an address to reach it.
- **Stopping** an instance raises an ACPI power button event, which k0smos
  honours, so a provider-initiated stop is graceful.
