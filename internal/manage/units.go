package manage

const UnitText = `[Unit]
Description=GOST proxy supervisor (gost-deploy)
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=simple
User=gost
Group=gost
ExecStart=/usr/local/sbin/gostctl _runtime
RuntimeDirectory=gost-deploy
RuntimeDirectoryMode=0700
Restart=on-failure
RestartSec=2
TimeoutStopSec=20
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
AmbientCapabilities=
StandardOutput=journal
StandardError=journal
SyslogIdentifier=gost

[Install]
WantedBy=multi-user.target
`
const APIUnitText = `[Unit]
Description=GOST deployment local management API
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/sbin/gostctl _api
Restart=on-failure
RestartSec=2
TimeoutStopSec=180
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true

[Install]
WantedBy=multi-user.target
`
