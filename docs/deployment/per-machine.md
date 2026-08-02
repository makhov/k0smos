# Per-machine configuration

Everything is on the kernel cmdline; see the
[options table](../reference/cmdline.md). A typical fleet line:

```
console=ttyS0 k0smos.ip=dhcp k0smos.hostname=node-07
```

With DHCP this is identical on every machine except the hostname, which is what
makes fleet deployment practical. Static addressing needs a unique line per
machine:

```
k0smos.ip=10.0.0.20/24 k0smos.gw=10.0.0.1 k0smos.dns=10.0.0.53
```
