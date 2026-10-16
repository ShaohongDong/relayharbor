ARG BASE=debian:12-slim
FROM ${BASE}
ENV container=docker DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    systemd systemd-sysv python3 ca-certificates iproute2 procps passwd \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl mask systemd-resolved.service systemd-logind.service \
       getty.target console-getty.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
