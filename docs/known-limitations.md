# Limits

What k0smos deliberately does not do, so you can design around it.

## No in-place upgrades

There is no A/B update, no package manager, and no API to change a running
system. To move to a new k0s release, deploy the new artifact and replace the
machines through whatever rolls them out — Cluster API, your hypervisor, or
`machine rm` followed by `machine up` locally.

## No shell, no SSH

A running node cannot be logged into. Everything a node has to say goes to its
console, and everything it needs to be told arrives on its cloud-init drive
before boot.

Plan for console access on every machine: `k0smosctl machine logs` locally,
the VMI serial console on KubeVirt, serial-over-LAN or the BMC elsewhere.
Application logs stay Kubernetes logs — collect them through the cluster.

## Cloud-init is a subset, and nothing is executed

`write_files`, the hostname, the network settings and the k0s role are applied.
A small set of `runcmd` verbs is *interpreted* — `mkdir`, `chmod`, `chown`,
`ln -s`, and `k0s install` — but no command from a drive is ever executed as a
process.

Bootstrap data that depends on a shell, package installation, `curl`, pipes or
redirection will not work. Unrecognised entries are reported on the console as
`UNSUPPORTED runcmd` and skipped. See
[machine configuration](usage/cloud-init.md).

## Everything in the node runs as root

The image is kept small by immutability and the absence of general-purpose
tools, not by process-level isolation.

## k0smosctl is for local machines

`k0smosctl` manages machines it started under QEMU on this host. It does not
manage remote KubeVirt VMs or physical servers; use the platform's own tooling
and obtain cluster access from the management cluster or the API endpoint.

## One k0s release per k0smos release

There is no separate OS version to pair with a k0s version, and no way to run a
k0s version other than the one inside the artifact. Choosing a k0s release means
choosing a k0smos release.

## No public-cloud images

There is no AMI, Azure image, or GCP image, and no client for cloud instance
metadata services. Platforms that can boot a UEFI qcow2 or raw disk and attach a
cloud-init drive will work; the rest are not covered.
