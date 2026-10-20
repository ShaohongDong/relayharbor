# gost-deploy

Ubuntu/Debian 原生 GOST v3 SOCKS5 服务管理器，支持直连、HTTP/SOCKS5 混合代理池、轮询、自动故障切换、定时探测和不中断已有连接的 profile 切换。提供本机管理 API，供 CLI 及后续 Vue 前端使用。

支持 Ubuntu 20.04/22.04/24.04、Debian 12/13，amd64/arm64、Python 3.8+ 和 systemd。运行时仅依赖 Python 标准库及校验后的官方 GOST，默认版本为 3.3.0。

## 安装

```bash
./gostctl init --output gost.json
./gostctl check --config gost.json
sudo ./install.sh --config gost.json
sudo gostctl status
```

最小系统可先安装 `python3 ca-certificates`。`install.sh` 会补齐缺失的系统依赖。`init` 和 `check` 无需 root；其他命令要求 root。默认监听 `0.0.0.0:1080`，入口强制用户名密码认证，随机密码只保存在权限为 `0600` 的配置文件内。客户端使用 SOCKS5，建议选择远端 DNS，例如 `curl --proxy socks5h://SERVER:1080 --proxy-user proxy https://example.com/`。

安装器下载正式发布包并校验官方 SHA-256；默认版本另有仓库锁定校验值。两个离线参数必须一起提供：

```bash
sudo ./install.sh --config gost.json \
  --archive /path/to/gost_3.3.0_linux_amd64.tar.gz \
  --checksums /path/to/checksums.txt
```

原生 GOST JSON（如 `examples/socks5.json`）仍可导入，自动转换为 `direct` profile。已安装旧版时，使用本仓库的 `install --config ...` 原地迁移；首次迁移会重启服务。迁移启动失败会恢复原 unit、管理入口、配置、二进制和启停状态。

## 代理池与 profile

使用 [examples/profiles.json](examples/profiles.json) 作为配置参考，替换全部 `CHANGE_ME` 凭据及示例地址。一个文件保存共享入口和多个 profile：

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

- `direct` 由服务器直接访问客户端指定目标。`pool` 每次连接选择一个上游，健康节点按顺序轮询，允许混合 HTTP 和 SOCKS5。
- HTTP 上游使用 CONNECT 隧道，支持承载 HTTPS 等 TCP 应用；这里的 `http` 不表示到上游的 TLS 连接。
- 上游可省略 `auth` 或设为 `null`；入口认证始终必需。上游支持域名、IPv4、`[IPv6]:port`，端口范围 1–65535；入口只允许 IP 及端口 1024–65535。
- 一次建连失败就摘除节点，并尝试本次请求尚未尝试过的其他健康节点；每节点最多 5 秒，总建连预算 15 秒。应用数据在连接成功后才转发，不重放已建立连接的数据。
- 全池故障时返回失败，保持当前 profile；不会改用直连或其他池。故障节点至少冷却 10 秒，再由探测成功恢复。
- 所有池均定时探测，包括未选中的池。探测经该节点的真实认证及 CONNECT 到目标，验证完整路径。TCP 探测成功表示目标连接已建立。
- HTTP 检查使用 `{"type":"http","url":"https://your-target.example/health","expectedStatus":200,"interval":30,"timeout":5}`，验证 HTTP 状态；不跟随重定向，HTTPS 校验证书。探测 URL 不允许凭据。目标需由操作者显式配置。
- 一次探测失败摘除，一次成功恢复；`interval` 为 1–86400 秒，`timeout` 为 0.1–60 秒。显式探测也遵循冷却时间。

```bash
sudo gostctl profile list
sudo gostctl profile use office
sudo gostctl profile probe office
sudo gostctl profile use direct
```

`use` 等待运行时确认后返回。返回后开始转发的新请求使用新 profile，之前已开始建连的请求和已有连接继续使用原路径；入口 PID 不变。允许选择暂不可用的池，此时返回 `available:false`，新连接失败，等待恢复。

只修改 profile、节点或探测配置时，`configure` 同样使用热更新。更换或删除节点后，其旧 worker 在旧连接自然结束后退出，不强制断开。已连接的上游本身故障会结束该连接，自动切换适用于新建连接。

## 更新、服务与恢复

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

管理配置为 `/etc/gost-deploy/config.json`（root `0600`）；生成的原生入口配置为 `/etc/gost/gost.json`（root:gost `0640`）。不要直接编辑生成配置或安装状态。安装后代码独立存储，原仓库可以移除。重新使用新版仓库执行相同配置的 `install` 可更新管理模块；模块未变化时保持幂等。

入口、调度器和 worker 由 `gost.service` 的非特权 supervisor 管理；每个不同节点一个 GOST worker，另有直连 worker和稳定入口进程。worker 崩溃会自动重启。入口或 supervisor 故障由 systemd 重启整组服务，此类故障会中断连接。原始 GOST 日志受限保存在 `/run/gost-deploy/`，随服务重启清理；生命周期错误可查 `journalctl -u gost`。

管理 API 独立运行于 `gost-deploy-api.service`。`configure` 和 profile 命令共用该 API；API 停止不影响已有转发，管理命令会明确报错。可用 `sudo systemctl restart gost-deploy-api` 恢复。

监听地址、入口账号、UDP 开关或 GOST 版本变更需要重启并断开连接。更新保留原运行及开机启动状态；原本停止的服务在更新后仍停止。每次有效更新（含 profile 切换）保存一层回滚记录；无变化不会重启或更新记录。更新失败自动恢复，确认丢失时也尝试恢复运行时代次。断电、SIGKILL 和宿主故障不提供跨文件事务保证。回滚旧配置/二进制继续使用当前管理架构。

## UDP 与网络

直连默认关闭 UDP；`init --udp` 或 `listener.udp:true` 开启标准 SOCKS5 UDP ASSOCIATE。代理池始终拒绝 UDP；切换到池后，之前建立的直连 UDP 会话可继续至关闭。

客户端 UDP 中继仍由公网入口 GOST 创建，内部使用 GOST UDP-over-TCP 到直连 worker。中继端口动态分配，仅放行 UDP 1080 不足以保证可用；需要按实际中继端口配置防火墙和 NAT。本工具不修改 UFW、nftables、云安全组或端口映射。

SOCKS5 认证及普通 HTTP/SOCKS5 上游握手不加密。需要保护这些链路时使用已有 SSH/VPN；HTTPS 应用仍有自身 TLS 保护。

## 管理 API 与 Vue 接入

API 默认为 `http://127.0.0.1:18080/api/v1`，强制 `Authorization: Bearer <token>`。随机 token 位于 `/var/lib/gost-deploy/api-token`，权限 `0600`。默认关闭跨域，不允许通过查询参数传 token。后续 Vue 可通过可信的同源后端代理接入；不要把管理 token 放进前端构建产物。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | `/status` | 服务状态、代次、当前 profile、可用性 |
| GET | `/profiles` | profile 列表及节点状态 |
| GET | `/nodes` | 节点健康、最近探测时间、延迟和失败计数 |
| GET | `/config` | 脱敏配置，响应带 ETag |
| POST | `/config/validate` | 静态校验候选配置，不安装或联网探测 |
| PUT | `/config` | 事务更新完整管理配置 |
| PUT | `/active-profile` | `{"name":"office"}`，等待切换确认 |
| POST | `/profiles/{name}/probe` | 立即探测，遵循故障冷却 |

完整接口定义见 [OpenAPI](docs/openapi.json)。成功响应为 `{"data":...,"revision":"..."}`，错误为 `{"error":{"code":"...","message":"..."}}`。PUT 必须携带读取时的 `If-Match: "revision"`；缺少时返回 428，过期时返回 409。

配置读取不返回密码，使用 `passwordSet` 表示已设置。更新时省略密码会按入口用户名或 profile 名称＋节点 ID＋上游用户名保留原密码；新身份必须显式提供密码。`auth:null` 明确移除上游认证。通过完整配置 PUT 增删 profile 和节点；删除当前 profile 时必须同时选择另一个现有 profile。

## 卸载与开发验证

```bash
sudo gostctl uninstall
sudo gostctl uninstall --purge

python3 -m unittest discover -s tests -p 'test_*.py' -v
bash -n install.sh tests/run-integration.sh
python3 tests/fetch_releases.py
python3 tests/runtime_integration.py
bash tests/run-integration.sh
```

默认卸载保留管理配置、模块、账号、token、缓存和恢复资料；`--purge` 清除本工具拥有的资源。工具拒绝覆盖外部二进制、账号、unit、drop-in 或管理模块路径。

本机协议测试只使用回环地址，无需 root。完整矩阵需要 Docker 和 `qemu-aarch64-static`，使用私有 systemd 容器，不挂载宿主 `/etc`、`/run` 或 cgroup。ARM64 QEMU 包括直连和代理池协议检查，不等同于 ARM64 实机 systemd 验收。结果与限制见 [验证记录](docs/validation.md)。
