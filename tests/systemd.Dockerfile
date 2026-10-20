ARG BASE=debian:12-slim
FROM ${BASE}
ENV container=docker DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    systemd systemd-sysv ca-certificates iproute2 procps passwd libc6-arm64-cross \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl mask systemd-resolved.service systemd-logind.service \
       getty.target console-getty.service
STOPSIGNAL SIGRTMIN+3
RUN if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
      sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources; \
    fi
CMD ["/sbin/init"]
