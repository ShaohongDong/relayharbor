# RelayHarbor

Go 实现的 Ubuntu/Debian GOST v3 服务管理器，支持认证 SOCKS5 入口、直连、HTTP/SOCKS5 混合代理池、轮询、故障切换、定时端到端探测和保留已有连接的 profile 热切换。提供内置 Vue 单页面控制台、管理 API、Nginx HTTPS 与自动证书，以及基于 sing-box 的 AnyTLS 多用户入口和 Mihomo 订阅。

支持 Ubuntu 20.04/22.04/24.04、Debian 12/13，Linux amd64/arm64 和 systemd。生产 Go 程序只使用标准库，目标服务器无需 Node.js、Go 或 Python。转发内核继续使用独立的官方 GOST 二进制，默认 3.3.0。

项目名称为 RelayHarbor；为保持现有部署兼容，管理命令仍为 `gostctl`，systemd 服务名、安装目录和配置路径沿用原命名。

## 安装与构建

解压对应架构的发行包，先校验下载包的 `SHA256SUMS`，再执行：

```bash
./bin/gostctl version
./bin/gostctl init --output gost.json
./bin/gostctl check --config gost.json
sudo ./install.sh --config gost.json
sudo gostctl status
```

安装器验证包内 `bin/SHA256SUMS`，补齐 CA 证书和账号管理工具，下载并校验官方 GOST。默认版本另有仓库锁定 SHA-256。离线安装必须同时提供：

```bash
sudo ./install.sh --config gost.json \
  --archive /path/to/gost_3.3.0_linux_amd64.tar.gz \
  --checksums /path/to/checksums.txt
```

源码仓库：[ShaohongDong/relayharbor](https://github.com/ShaohongDong/relayharbor)。源码开发使用 Go 1.27.2、Node.js 24 LTS 和 Make：

```bash
git clone git@github.com:ShaohongDong/relayharbor.git
cd relayharbor
make build
make check
make test
make test-race
make release VERSION=0.1.0
```

`make release` 在 `dist/` 生成 `relayharbor_<version>_linux_<arch>.tar.gz` 静态程序发行包和 SHA-256 清单（amd64/arm64）。可用 `make GO=/path/to/go build` 指定工具链。CI 生成构建产物，不自动对外发布。

这是尚无正式部署的新项目：只接受 `schemaVersion:2` 配置，不导入旧原生 GOST JSON，不迁移 Python 版安装状态。若测试机器残留旧版安装，先使用旧版工具清理，不直接覆盖外部或不支持的资源。

`init` 和 `check` 无需 root；服务管理命令要求 root。默认监听 `0.0.0.0:1080`，强制入口认证，随机密码只保存在权限 `0600` 的配置文件中。客户端可使用 `socks5h://SERVER:1080`，由代理端解析域名。

## 代理池与 profile

参考 [examples/profiles.json](examples/profiles.json)，替换全部 `CHANGE_ME` 凭据、代理地址和探测目标。一个文件保存共享入口与多个 profile：

```json
{
  "schemaVersion": 2,
  "listener": {
    "addr": "0.0.0.0:1080",
    "udp": false,
    "users": [{"username": "proxy", "password": "CHANGE_ME_entry_password"}]
  },
  "activeProfile": "direct",
  "profiles": [
    {"name": "direct", "type": "direct"},
    {
      "name": "office",
      "type": "pool",
      "nodes": [
        {"id": "http-a", "type": "http", "addr": "proxy-a.example:8080"},
        {"id": "socks-b", "type": "socks5", "addr": "proxy-b.example:1080",
         "auth": {"username": "upstream", "password": "CHANGE_ME_upstream_password"}}
      ],
      "probe": {"type": "tcp", "addr": "your-target.example:443", "interval": 30, "timeout": 5}
    }
  ]
}
```

- `direct` 直接访问客户端指定目标。`pool` 每次连接选择一个上游，健康节点依次轮询，允许混合 HTTP 和 SOCKS5。HTTP 上游使用 CONNECT；到上游本身的连接不启用 TLS。
- 上游 `auth` 可以省略或设为 `null`；入口认证始终必需。上游允许域名、IPv4、`[IPv6]:port`，端口 1–65535；入口只允许 IP，端口 1024–65535。
- 建连失败立即摘除节点，尝试本次请求尚未尝试的其他健康节点；每节点最多 5 秒，总预算 15 秒。应用数据只在建连成功后转发，不重放已建立连接的数据。
- 全池故障返回失败，保留当前 profile，不改用直连或其他池。故障节点至少冷却 10 秒，探测成功后恢复。
- 所有池均定时探测，包括未选中的池。探测通过节点的真实认证和 CONNECT 到显式配置的目标，验证完整路径。TCP 探测成功表示已建立目标连接。
- HTTP 检查使用 `{"type":"http","url":"https://your-target.example/health","expectedStatus":200,"interval":30,"timeout":5}`。校验状态码，不跟随重定向；HTTPS 校验证书，URL 不允许凭据或片段。
- 一次失败摘除，一次成功恢复；探测间隔 1–86400 秒，超时 0.1–60 秒，默认 30/5 秒。显式探测也遵循冷却时间，并发探测最多 16 个。

```bash
sudo gostctl profile list
sudo gostctl profile use office
sudo gostctl profile probe office
sudo gostctl profile use direct
```

`use` 等待运行时确认后返回。返回后开始转发的新请求使用新 profile，之前已开始建连的请求和已有连接继续使用原路径，入口 PID 不变。允许选择暂不可用的池，状态显示 `available:false`，新请求失败并等待恢复。

只修改 profile、节点或探测配置时，`configure` 也使用热更新。删除或更换节点后，其旧 worker 等待旧连接自然结束再退出。上游本身故障会结束已有连接，自动切换作用于新建连接。

## 生命周期与部署架构

```bash
sudo install -m 600 /etc/gost-deploy/config.json /root/gost-candidate.json
sudo editor /root/gost-candidate.json
sudo gostctl check --config /root/gost-candidate.json
sudo gostctl configure --config /root/gost-candidate.json
sudo gostctl start
sudo gostctl stop
sudo gostctl restart
sudo gostctl status
sudo gostctl logs --follow
sudo gostctl upgrade --version 3.3.0
sudo gostctl rollback
```

管理配置 `/etc/gost-deploy/config.json` 为 root `0600`；生成的 `/etc/gost/gost.json` 和 `/etc/gost/runtime.json` 为 root:gost `0640`。不要直接编辑生成配置或安装状态。

同一 Go 程序运行两个服务：`gost.service` 以 `gost` 用户运行 supervisor、调度器及 GOST 子进程；`gost-deploy-api.service` 运行本机管理 API。它们经权限 `0600` 的 Unix socket 通信。API 停止不影响转发，可用 `sudo systemctl restart gost-deploy-api` 恢复。

每个节点配置对应一个 GOST worker，另有直连 worker 和稳定 GOST 入口。worker 崩溃会重启；入口或 supervisor 故障由 systemd 重启整组，已有连接会中断。受限原始日志位于 `/run/gost-deploy/`，服务重启后清理；生命周期错误查看 journal。

管理器按 SHA-256 存储于 `/opt/gost-deploy/managers/<hash>/gostctl`，稳定入口为 `/usr/local/sbin/gostctl`。重新使用新版发行包执行相同配置的安装命令，可更新管理器；先排空 API 请求，再替换程序并重启服务。完全相同的安装保持幂等。安装完成后可删除源码和发行包目录。

监听地址、入口账号、UDP 开关、GOST 版本或管理器程序变更需要重启。更新保留原运行和开机启动状态；每次有效配置/版本更新保存一层回滚记录。失败自动恢复；热更新确认丢失时恢复磁盘与运行时代次。API 退出会等待在途事务完成。断电、SIGKILL 和宿主故障不提供跨文件事务保证。

## UDP 与网络

直连默认关闭 UDP，`init --udp` 或 `listener.udp:true` 开启标准 UDP ASSOCIATE。代理池拒绝 UDP；之前建立的直连 UDP 会话可继续至关闭。

公网 GOST 入口动态分配 UDP 中继端口，内部使用 GOST UDP-over-TCP 到直连 worker。仅放行 UDP 1080 不足以保证可用，需按实际中继端口配置防火墙和 NAT。本工具不修改防火墙或云安全组。SOCKS5 认证及普通 HTTP/SOCKS5 上游握手不加密，需要保护链路时使用 SSH/VPN；HTTPS 应用保留自身 TLS。

## 管理 API 与 Vue

API 地址为 `http://127.0.0.1:18080/api/v1`，必须携带 `Authorization: Bearer <token>`。随机 token 位于 `/var/lib/gost-deploy/api-token`，权限 `0600`。内置 Vue 控制台与 API 同源，不启用 CORS，不接受查询参数 token，不应把管理 token 放入构建产物。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | `/status` | 服务状态、revision、当前 profile 和可用性 |
| GET | `/profiles` | profile 列表与节点状态 |
| GET | `/nodes` | 健康、最近探测、延迟及失败计数 |
| GET | `/config` | 脱敏配置及 ETag |
| POST | `/config/validate` | 静态验证，不安装或联网 |
| PUT | `/config` | 事务更新完整配置 |
| PUT | `/active-profile` | `{"name":"office"}`，等待确认 |
| POST | `/profiles/{name}/probe` | 显式探测，遵循冷却 |

[OpenAPI 定义](docs/openapi.json) 保持原契约。成功响应 `{"data":...,"revision":"..."}`；失败响应 `{"error":{"code":"...","message":"..."}}`。PUT 必须带 `If-Match: "revision"`，缺失返回 428，过期返回 409。

读取配置使用 `passwordSet` 隐藏密码。更新时省略密码，按入口用户名或 profile 名称＋节点 ID＋上游用户名保留原密码；新身份必须提供密码。`auth:null` 移除上游认证。删除当前 profile 必须同时选择另一个现有 profile。

## 卸载与验收

```bash
sudo gostctl uninstall
sudo gostctl uninstall --purge
make fetch
make integration
```

默认卸载保留配置、管理器、账号、token 和缓存；`--purge` 清除受管资源。工具拒绝覆盖外部二进制、账号、unit 或 drop-in。

单元测试使用 Go；真实协议测试使用回环地址和校验后的 GOST、sing-box、Mihomo。完整矩阵需要 Docker、Go 和 QEMU ≥ 8.2 的 `qemu-aarch64-static`，可用 `GOST_TEST_QEMU_BIN=/absolute/path/qemu-aarch64-static make integration` 指定模拟器。宿主旧版 QEMU 6.2 在 AnyTLS ARM64 内核上出现过运行时异常，不用于本轮验收。测试镜像提供 ARM64 glibc，不需要 Go 或 Python，不挂载宿主 `/etc`、`/run` 或 cgroup。ARM64 QEMU 同时运行管理器、GOST、sing-box 与 Mihomo，不等同于实机 systemd 验收。源码、模拟器、二进制及日志哈希记录于忽略的 `test-results/`，结果见 [验证记录](docs/validation.md)。

## Vue 单页面控制台

安装新版发行包后访问 `http://127.0.0.1:18080/`。控制台采用中文浅色紧凑表格，包含“代理池与节点”“入口与认证”“配置预览”“客户端订阅”“服务运维”五个页内标签；代理池和节点在抽屉中编辑。

在服务器读取认证 token：

```bash
sudo cat /var/lib/gost-deploy/api-token
```

将 token 输入页面即可连接。Token 和未应用配置仅保存在页面内存；刷新页面需要重新认证。关闭或刷新有草稿的页面会提示，断开认证会清除草稿和密码。请勿将 token 写入 URL、前端环境变量或构建产物。

远程服务器通过 SSH 隧道访问，浏览器打开本机 `http://127.0.0.1:18080/`：

```bash
ssh -N -L 127.0.0.1:18080:127.0.0.1:18080 user@server
```

若本机端口被占用，可将第一个 `18080` 改为其他端口。管理服务仍只监听服务器回环地址，不启用 CORS；也可启用下述 Nginx 域名 HTTPS 部署。

配置编辑先保存为页面草稿，点击“校验并应用”才会提交。状态每 5 秒刷新，页面隐藏或操作进行时暂停，刷新不会覆盖草稿。密码留空表示保留原身份的密码；改变 profile 名称、节点 ID、所属池或认证用户名后，需要重新输入相应密码。预览隐藏所有密码。CLI 或其他页面改变配置时，控制台保留草稿并阻止覆盖，需重新加载后编辑。

服务运维支持启动、停止、重启和恢复上一份配置与 GOST 版本；需要中断连接的操作会二次确认。停止转发服务不会停止管理页面。近期日志固定读取两个受管服务的 journal，默认 100 条、最多 500 条/256 KiB、5 秒读取超时；不开放 worker 原始日志，已知受管凭据和认证头会被屏蔽，不对历史任意文本中的未知凭据作识别保证。

新增 API：`POST /service/start`、`POST /service/stop`、`POST /service/restart`、`POST /rollback` 均提交 `{}`，必须带 `If-Match`；`GET /logs?limit=100` 查询近期日志。`/status` 增加脱敏回滚目标摘要。所有接口继续使用 `/api/v1` 前缀和 Bearer 认证。不提供网页在线升级、卸载或多用户登录；HTTPS 访问沿用管理员 Bearer token。

### Clash/Mihomo 订阅与 AnyTLS

支持向 **支持 AnyTLS 的 Mihomo 客户端**发布订阅。每个用户或设备有独立密码、订阅 token、TCP 端口及 systemd 实例；客户端只看到一个节点，出站跟随服务端当前 `activeProfile`。首版仅 TCP，不导入外部订阅。普通 Clash 内核不作为兼容目标。

先完成下述 Nginx HTTPS 部署，再执行：

```bash
sudo gostctl anytls setup
# 离线：sudo gostctl anytls setup --sing-box-archive /path/sing-box-1.14.3-linux-amd64.tar.gz
sudo gostctl anytls user add --id phone --name 我的手机
sudo gostctl anytls user add --id laptop --name 笔记本 --port 8444
sudo gostctl anytls status
sudo gostctl anytls user credentials --id phone
```

管理器下载并校验固定版本 sing-box 1.14.3（Linux amd64/arm64，官方归档 SHA-256 锁定在源码），作为独立进程使用；目标机无需额外语言运行时，ARM64 内核使用受支持 Ubuntu/Debian 自带的 glibc。AnyTLS 入口监听 IPv4，域名需有可达的 A 记录。默认从 8443 起分配未占用端口，分配后持久化；需在防火墙及云安全组放行实际分配的 TCP 端口。支持最多 128 个用户，每个启用用户运行一个进程，部署容量应按实际内存和连接数评估。

Vue 单页面的“客户端订阅”标签支持新增、重命名、启停、删除、查看凭据、复制链接及下载配置。首次安装内核使用 CLI。凭据只在显式查看时显示，不写入浏览器持久化存储；通过 HTTPS 页面下载配置。可将完整配置链接导入客户端，或在现有配置的 `proxy-providers` 中使用节点列表链接：

- `https://DOMAIN/sub/TOKEN/clash.yaml`：完整配置，单节点选择组与 `MATCH` 路由，无自动直连兜底。
- `https://DOMAIN/sub/TOKEN/proxies.yaml`：仅 `proxies` 节点列表，供现有 Mihomo 配置使用。

订阅包含对应用户的 AnyTLS 密码，应视为凭据保管。token 与管理员 Bearer token 相互独立，不授予管理权限。订阅不缓存，受管 Nginx 不记录订阅路径；HTTP 请求直接拒绝，不重定向含 token 的 URL。节点使用已配置的域名与 SNI，必须校验证书，`udp:false`；服务端也拒绝 UDP。

```bash
sudo gostctl anytls user rename --id phone --name 新名称
sudo gostctl anytls user reset-token --id phone
sudo gostctl anytls user reset-password --id phone
sudo gostctl anytls user disable --id phone
sudo gostctl anytls user enable --id phone
sudo gostctl anytls user delete --id phone
sudo gostctl anytls disable
sudo gostctl anytls enable
```

重置链接立即撤销旧订阅链接，保留密码及已有连接；重置密码仅重启该用户实例，需客户端更新订阅。禁用或删除同时撤销订阅访问、停止实例并终止该用户连接。其他用户保持原连接和 PID。全局启停影响全部用户。代理池切换复用原调度器：已有连接保留，新请求使用新 profile；全池故障不绕过调度器直连。

AnyTLS 状态位于 `/var/lib/gost-deploy/anytls.json`（0600），使用独立 revision；生成配置和证书副本位于 `/etc/gost/anytls/`，仅 root 和专用 `gost` 服务组可读。实例名为 `gost-deploy-anytls-USER.service`，以 `gost` 账号运行，执行前校验内核与配置。查看启动错误可用 `journalctl -u gost-deploy-anytls-USER`；不记录代理会话日志。

证书复用现有 lego 续期流程，验证后更新实例证书副本并探测实际提供的证书；热加载失败恢复旧版本并报告失败，不重启全部用户兜底。AnyTLS 启用期间需先停用它，才能停用 HTTPS 或修改域名。管理 API 停止不影响转发；GOST 停止时新代理请求失败，恢复后可重新连接。卸载先停用全部实例，`--purge` 清理受管用户、凭据和内核。

管理接口为 `GET /api/v1/anytls`，以及 `/api/v1/anytls/enable|disable|users`、`/api/v1/anytls/users/{id}/rename|enable|disable|delete|reset-token|reset-password|credentials` 的 POST 操作。所有操作使用管理员 Bearer token；POST 必须携带 **AnyTLS 自身**的 `If-Match`，不使用代理配置 revision。用户列表脱敏，`credentials` 是显式返回秘密的接口。详见 OpenAPI。

### Nginx HTTPS 与免费证书

支持在**专用新服务器**上一键安装 Nginx，使用 Let's Encrypt 免费证书代理 Vue 和管理 API；SOCKS5 入口仍按原配置监听。目标机无需 Python。域名需有正确的 A/AAAA 记录，公网 TCP 80/443 需持续可达，服务器能通过 HTTPS 访问 GitHub 和 ACME 服务。本工具不修改 DNS、防火墙或云安全组。

对已安装的 GOST 执行：

```bash
sudo gostctl tls setup --domain panel.example.com --email admin@example.com --accept-tos
sudo gostctl tls status
```

或者首次安装时同时启用 HTTPS：

```bash
sudo ./install.sh --config gost.json \
  --domain panel.example.com --email admin@example.com --accept-tos
```

`--accept-tos` 表示接受 [Let's Encrypt 服务条款](https://letsencrypt.org/repository/)。首次发现已有 Nginx 安装、配置或 80/443 被占用时拒绝接管。本工具管理的部署可重复执行；相同 setup 命令也用于失败后重试。改变域名需再次运行 setup，成功签发后切换，失败保留原站点。

浏览器打开 `https://panel.example.com/`，输入现有管理 token。Nginx 终止 TLS，管理服务继续只监听 `127.0.0.1:18080`；证书成功签发前不提供明文管理页面。HTTP-01 验证路径保留在 80 端口，其余请求在启用 HTTPS 后重定向到固定域名。

证书客户端为固定版本 lego 5.5.2，下载包经过官方清单与仓库锁定 SHA-256 校验。可使用 `--lego-archive FILE --lego-checksums FILE` 提供离线客户端包，amd64/arm64 均有固定校验值；**签发和续期仍需要联网访问 ACME**。这些参数也可与安装参数一起使用。

`gost-deploy-tls-renew.timer` 每 12 小时检查一次，附加最多一小时随机延迟，并在关机错过检查后补执行。是否续期由 lego 根据证书实际有效期及 CA 的 ARI 建议判断；不会每次检查都重新签发。定时任务独立于页面和管理 API，获取证书后验证域名、有效期与密钥匹配，再通过 `nginx -t` 并平滑重载。失败保留原有效证书，下次自动重试；已经获取但尚未部署的证书优先重试部署。

“服务运维”的 HTTPS 卡片展示证书有效期、timer、最近结果及后台任务，支持检查续期和重载。首次配置、域名修改、停用由 CLI 完成：

```bash
sudo gostctl tls renew      # 后台检查续期，不强制提前签发
sudo gostctl tls reload     # 后台校验并重载 Nginx
sudo gostctl tls disable    # 停用站点和续期，保留证书与账号
sudo systemctl status gost-deploy-tls-renew.timer nginx
sudo journalctl -u gost-deploy-tls-renew -u gost-deploy-tls-reload -n 100 --no-pager
```

管理元数据 `/etc/gost-deploy/tls.json`、ACME 账号与证书 `/var/lib/gost-deploy/tls/` 仅 root 可读。不要手动修改受管站点 `/etc/nginx/conf.d/gost-deploy.conf` 或 systemd units；发现外部修改会拒绝覆盖。管理器升级与卸载遇到正在进行的 TLS 操作会提示稍后重试。

卸载同步停用 HTTPS，`uninstall --purge` 清理本项目证书、账号及受管资源，保留系统 Nginx 软件包。HTTPS 出现问题时仍可通过原 SSH 隧道恢复管理；首次 HTTPS 失败不撤销已成功安装的 GOST。

新增 `GET /api/v1/tls`、`POST /api/v1/tls/renew` 和 `POST /api/v1/tls/reload`；POST 提交 `{}` 并携带 `If-Match`，立即返回后台任务已排队，随后查询状态。TLS 操作不改变代理配置 revision；并行操作返回 `409 tls_busy`。API 不提供私钥、域名配置或强制签发操作。

### 前端开发与构建

源码构建需要 Node.js 24 LTS 或更新版本及 Go 1.27.2；**目标服务器无需 Node.js、Go 或 Python**。前端依赖由 `web/package-lock.json` 锁定，产物嵌入 Go 程序，页面不依赖 CDN。

```bash
make build                 # npm ci（依赖变化时）、类型检查、前端构建、Go 静态程序
make web-test              # 前端模型与 API 单元测试
make web-e2e               # 浏览器交互测试，需要先安装 Chromium
make test                  # 前端与 Go 测试
make test-race
make integration           # 五个 systemd + 两组 ARM64 + 真实浏览器链路
make release
cd web && npm run dev      # Vite 本地开发，代理 /api 到 127.0.0.1:18080
```

首次浏览器测试运行 `cd web && npx playwright install --with-deps chromium`。已有 Google Chrome 时可设置 `PLAYWRIGHT_CHANNEL=chrome`。独立运行 Go 命令前先执行 `make web-build` 生成嵌入资源。隔离集成测试通过仅绑定宿主回环地址的测试桥接访问容器内管理服务，模拟 SSH 隧道；生产代码不包含该桥接。
