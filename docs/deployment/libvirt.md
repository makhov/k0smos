# KVM / libvirt

The closest target to what is verified. Direct kernel boot, so no bootloader is
involved:

```xml
<os>
  <type arch='x86_64' machine='q35'>hvm</type>
  <kernel>/var/lib/k0smos/vmlinuz</kernel>
  <initrd>/var/lib/k0smos/k0smos-initramfs.gz</initrd>
  <cmdline>console=ttyS0 k0smos.ip=dhcp</cmdline>
</os>
<devices>
  <disk type='file' device='disk'>
    <driver name='qemu' type='raw'/>
    <source file='/var/lib/k0smos/k0smos.img'/>
    <target dev='vda' bus='virtio'/>
  </disk>
  <interface type='network'>
    <source network='default'/>
    <model type='virtio'/>
  </interface>
  <!-- Optional: shutdown channel, if you would rather not rely on ACPI. -->
  <channel type='unix'>
    <source mode='bind' path='/var/lib/k0smos/control.sock'/>
    <target type='virtio' name='k0smos.control'/>
  </channel>
  <serial type='pty'><target port='0'/></serial>
</devices>
```

`virsh shutdown` works here: libvirt raises an ACPI power button event, which
k0smos honours. `virsh console` gives you the boot log.

With real KVM this boots considerably faster than the HVF setup used for
development.
