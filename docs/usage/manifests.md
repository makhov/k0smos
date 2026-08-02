# Ship Kubernetes manifests

k0s applies anything under `/var/lib/k0s/manifests/<stack>/`, so a manifest is just
a file — no shell, no `kubectl apply`, nothing to run on the node:

```bash
k0smosctl gen \
  --file ns.yaml:/var/lib/k0s/manifests/demo/ns.yaml \
  --file deployment.yaml:/var/lib/k0s/manifests/demo/deployment.yaml \
  -o dist/cidata.iso
```

The file must sit in a **subdirectory** of `manifests/`; that directory name is the
stack. k0smos writes it before starting k0s, so it is applied on the first
reconcile — and because nothing persists, it is rewritten and reapplied on every
boot, which makes it idempotent by design.

Writing the cloud-config yourself, which is what a Cluster API bootstrap provider
does, the same thing is:

```yaml
#cloud-config
write_files:
  - path: /var/lib/k0s/manifests/demo/ns.yaml
    permissions: "0644"
    encoding: gzip+base64
    content: <gzip -c ns.yaml | base64 -w0>
```

`gzip+base64` matters there and not here: a provider delivers user-data through a
Secret or a metadata service with size limits, whereas a drive written locally has
no practical limit, so `gen` uses plain base64.
