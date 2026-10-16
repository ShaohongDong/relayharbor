# gost-deploy

在 Ubuntu/Debian 上部署原生 GOST v3 SOCKS5 服务。提供随机密码、多用户认证、可选 UDP、systemd 管理和更新失败恢复，无需 Docker 或 Go 编译环境。

| 项目 | 支持范围 |
| --- | --- |
| 系统 | Ubuntu 20.04 / 22.04 / 24.04、Debian 12 / 13，运行 systemd |
| 架构 | amd64 / arm64 |
| 默认 GOST | 3.3.0；可显式指定其他正式 v3 版本 |
| 默认入口 | `0.0.0.0:1080`，强制用户名密码认证 |
| UDP | 默认关闭，可开启标准 SOCKS5 UDP ASSOCIATE |

## 快速安装

将仓库复制到目标服务器，然后执行：

```bash
# 如果最小系统尚未安装 Python：
sudo apt-get update
sudo apt-get install -y python3 ca-certificates

./gostctl init --output gost.json
# 按需修改监听 IP、端口和账号。随机密码保存在此文件中。
./gostctl check --config gost.json
sudo ./install.sh --config gost.json
sudo gostctl status
```

`install.sh` 可补齐 Python 3、CA 证书和账号管理工具；`init`、`check` 不要求 root。安装及其他管理命令要求 root。配置文件路径和其他参数可包含空格，不作为 Shell 代码执行。

安装程序下载官方发布包并校验同版本 `checksums.txt`；3.3.0 的 amd64/arm64 压缩包 SHA-256 额外锁定在代码中。不存在自动跟随 latest 的行为。相同版本、相同配置重复安装不会重启服务；改变现有配置请使用 `configure`，改变版本请使用 `upgrade`。

安装会启用 `gost.service` 开机启动，使用无登录权限的 `gost` 系统用户。配置位于 `/etc/gost/gost.json`（`root:gost 0640`），日志由 journald 管理。管理命令安装后不依赖仓库目录，源目录可移除。

## 客户端连接

在支持 SOCKS5 用户名密码认证的客户端中填写：

- 地址：服务器 IP。
- 端口：`1080`，或自定义端口。
- 用户名：默认 `proxy`。
- 密码：生成配置中的随机密码。
- DNS：开启“通过 SOCKS5 代理解析域名”，或使用客户端对应选项。

curl 的 `socks5h` 表示由代理服务器解析目标域名。以下方式会交互读取密码，无需将密码写入命令行：

```bash
curl --proxy socks5h://SERVER_IP:1080 --proxy-user proxy https://example.com/
```

标准 SOCKS5 的用户名密码握手不加密，也不会为应用流量增加加密层；HTTPS 应用内容仍由自身 TLS 保护。此配置关闭 GOST 的非标准协商加密扩展，便于普通 SOCKS5 客户端使用。需要保护客户端到代理之间的链路时，应通过已有 SSH/VPN 隧道连接。本仓库首版不配置 TLS 隧道。

## 多用户与配置更新

使用原生 GOST JSON。`examples/socks5.json` 展示双用户配置，其中 `CHANGE_ME` 凭据必须替换，工具拒绝直接安装占位凭据。

```bash
# 在受限目录内制作候选配置，避免直接编辑正在使用的文件。
sudo install -m 600 /etc/gost/gost.json /root/gost-candidate.json
sudo editor /root/gost-candidate.json
sudo gostctl check --config /root/gost-candidate.json
sudo gostctl configure --config /root/gost-candidate.json
```

在 `authers[0].auths` 中增删用户或修改密码。账号和密码各允许 1–255 个 UTF-8 字节，支持空格及特殊字符，拒绝控制字符、空值和重复用户名。配置保留一个 SOCKS5 服务、一个认证器；其他协议、认证绕过、外部配置源、API 和自定义 systemd drop-in 不在首版管理范围。

修改 `services[0].addr` 设置监听 IP/端口；支持 IPv4 和 `[IPv6]:端口`，非特权服务要求端口为 1024–65535。默认绑定所有 IPv4 地址，IPv6 需显式配置。目标机器必须实际拥有所绑定的地址。

### 开启 UDP

生成新配置时使用：

```bash
./gostctl init --output udp.local.json --udp
```

已有配置将 `services[0].handler.metadata.udp` 改为 `true`，然后执行 `configure`。`listener.type` 仍为 `tcp`，这是 SOCKS5 控制连接，不应改成 UDP。

SOCKS5 UDP 通过 TCP 握手分配 UDP 中继端口，中继端口可能动态变化。仅放行 TCP/UDP 1080 不足以保证 UDP 可用；防火墙必须允许受信客户端访问实际 UDP 中继端口。服务器在 NAT 后时，还必须保证返回的中继地址及端口可达。需要固定公网 UDP 映射的环境应先单独设计网络规则，本工具不会自动创建端口映射。

## 服务管理

```bash
sudo gostctl start
sudo gostctl stop
sudo gostctl restart
sudo gostctl status
sudo gostctl logs
sudo gostctl logs --follow
```

`status` 输出版本、架构、运行状态和开机启动状态的 JSON；服务未运行时退出码为 1。`logs` 显示最近 100 条日志。原始命令也可直接使用：

```bash
sudo systemctl status gost
sudo journalctl -u gost -n 100 --no-pager
```

`configure`、`upgrade` 会短暂停止服务并断开已有连接。候选配置先经过本工具策略检查和 GOST 原生解析，然后真实启动并逐一验证用户认证。成功后保留操作前的运行及开机启动状态；因此原本停止的服务在验证后仍保持停止。

## 升级、回滚与离线安装

```bash
sudo gostctl upgrade --version 3.3.0
sudo gostctl rollback
```

升级必须显式指定正式 v3 版本。每次成功更新配置或二进制，都保存上一套二进制、配置以及运行/开机启动状态。`rollback` 恢复上一套记录，可用于配置回退和版本回退；随后可再次 rollback 切换回去。没有上一个版本时明确报错。只保留一层恢复记录，旧下载缓存可能继续占用磁盘。

更新失败时自动恢复操作前的配置、二进制及服务状态。下载、校验或配置预检失败不会停止现有服务；若恢复也失败，命令返回错误并要求检查服务日志。此恢复覆盖命令执行期间捕获的失败和中断，不是断电或 `SIGKILL` 下的事务保证。

可从官方 [Releases](https://github.com/go-gost/gost/releases) 下载与目标系统架构匹配的压缩包及同版本 `checksums.txt`，离线传入：

```bash
sudo ./install.sh --config gost.json \
  --archive /path/to/gost_3.3.0_linux_amd64.tar.gz \
  --checksums /path/to/checksums.txt

sudo gostctl upgrade --version 3.3.0 \
  --archive /path/to/gost_3.3.0_linux_amd64.tar.gz \
  --checksums /path/to/checksums.txt
```

两个参数必须一起提供；其他版本的离线清单由操作者从官方渠道获取。已安装依赖时，离线安装不访问网络。

## 防火墙

工具不修改 UFW、nftables 或云安全组。根据实际来源 IP 添加规则，例如使用 UFW，仅允许一个客户端访问 TCP 入口：

```bash
sudo ufw allow from CLIENT_PUBLIC_IP to any port 1080 proto tcp
```

若 UFW 当前未启用，应先确保现有 SSH 管理规则完整，再按自己的服务器管理流程启用。云安全组需要对应放行。UDP 应依照前述中继行为配置，不能照搬 TCP 规则。

## 卸载

```bash
sudo gostctl uninstall
# 或同时清除本工具管理的配置、用户、二进制缓存和状态：
sudo gostctl uninstall --purge
```

默认卸载停止并禁用服务，移除 systemd unit 和 gost 入口，保留管理命令、配置、账号、缓存和恢复资料；可重新安装，也可稍后运行 `uninstall --purge` 完全清理。卸载后重新安装建立新的更新记录，不自动恢复旧服务的启停状态。

发现外部已有同名二进制、账号、unit 或 drop-in 时，安装/更新拒绝接管，不覆盖已有服务。不要手动改动受管二进制、unit、安装状态；配置更新使用候选文件加 `configure`，以保留可恢复版本。

## 开发与验证

```bash
python3 -m unittest discover -s tests -p 'test_*.py' -v
bash -n install.sh tests/run-integration.sh

# 完整隔离集成测试：要求 Docker、Python 3、qemu-aarch64-static。
bash tests/run-integration.sh
```

集成测试运行独立 systemd 特权容器，使用私有 cgroup 命名空间，不挂载宿主机 `/etc`、`/run` 或 cgroup 目录。只应在可信代码上运行此测试。下载缓存在 `.cache/`，原始结果在 `test-results/`，均不进入 Git。ARM64 测试运行真实官方二进制，通过 QEMU 验证认证及 TCP/UDP 转发；不将模拟测试视为 ARM64 实机 systemd 验收。

实际验证结果及限制见 [验证记录](docs/validation.md)。

上游参考：[GOST 配置与 systemd](https://gost.run/en/getting-started/configuration-overview/)、[SOCKS5](https://gost.run/en/tutorials/protocols/socks/)、[多用户认证](https://gost.run/en/concepts/auth/)。本仓库封装部署流程，不修改或随仓库分发 GOST 二进制。
