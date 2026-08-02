FROM ubuntu:24.04

ARG TARGETARCH
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       dosfstools e2fsprogs gdisk mtools qemu-utils grub-common \
    && case "$TARGETARCH" in \
         amd64) apt-get install -y --no-install-recommends grub-efi-amd64-bin ;; \
         arm64) apt-get install -y --no-install-recommends grub-efi-arm64-bin ;; \
         *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
       esac \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /repo
