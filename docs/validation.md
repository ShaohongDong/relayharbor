# 验证记录

## 多代理池与管理 API 改造（2026-10-20，Asia/Shanghai）

当前实现已拆分为配置模型、原生 GOST 配置生成、转发调度与进程监督、生命周期事务、控制通道及 HTTP API。运行时保持 Python 3.8+ 标准库依赖，默认使用校验后的 GOST 3.3.0；版本切换和旧版兼容场景同时使用官方 3.2.6 发布包。

本轮完整矩阵已通过，测试开始与结束时核对源码 SHA-256 一致。以下结果属于当前改造版本；后续章节保留历史版本记录，不能代替本轮证据。

| 环境 | 验证内容 | 结果 |
| --- | --- | --- |
| 宿主 Python 3.10 | 21 项单元测试，Shell/Python 语法、Python 3.8 语法兼容及空白检查 | 通过 |
| Ubuntu 20.04 / Python 3.8 | 同一组 21 项单元测试 | 通过 |
| Ubuntu 20.04、22.04、24.04 / amd64 systemd 容器 | 每系统 20 项生命周期场景、9 项真实转发场景、Bash 安装及容器重启后 TCP/UDP 检查 | 全部通过 |
| Debian 12、13 / amd64 systemd 容器 | 同上 | 全部通过 |
| Ubuntu 24.04、Debian 12 / ARM64 GOST + QEMU | 每环境 4 组原有认证/域名/TCP/UDP 场景及 9 项新增转发场景 | 全部通过 |

新增转发场景使用真实 GOST 二进制和本地协议目标，覆盖：

- 直连 TCP、域名和经内部 GOST 隧道的 UDP；代理池拒绝 UDP。
- 带认证的 HTTP CONNECT / SOCKS5 混合池轮询，以及无认证 HTTP 上游。
- 请求建连失败后自动尝试其他健康节点，全池故障时拒绝转发。
- 故障冷却、端到端定时探测恢复，以及 HTTP 状态码不匹配时摘除节点。
- 等待确认的 profile 热切换，直连和代理池的既有 TCP 连接保持可用。
- 无效候选配置保留当前运行代次，worker 崩溃恢复，删除节点后等待旧连接结束再退出 worker。

生命周期矩阵新增了 CLI → HTTP API → 运行时切换、管理模块独立更新，以及从旧版实际 unit/状态迁移。故障注入验证迁移失败恢复原管理入口、unit、配置和状态，保留原有运行/停止及启用/禁用状态保护。单元测试覆盖 API Bearer 认证、ETag/revision 冲突、密码脱敏与保留、错误输入、退出时等待在途事务，以及提交确认丢失后的磁盘与运行时恢复。

当前原始证据保存在 Git 忽略的 `test-results/`：`evidence.json` 记录源码及日志哈希、五系统场景数和两组 ARM64 模拟结果；`matrix.log` 与各环境日志保存完整输出；`static.log`、`python38-unit.log` 保存单元测试结果。可用 `bash tests/run-integration.sh` 重建完整矩阵。本轮未在宿主机安装或升级服务，也未执行远程 CI。

验证边界：systemd 容器共享宿主内核，容器重启不等同于宿主重启；ARM64 QEMU 协议验证不等同于 ARM64 实机 systemd 验收。外部真实代理服务、公网客户端、防火墙及 NAT 可达性仍取决于部署环境。监听地址、入口凭据、UDP 开关、二进制版本变更及首次旧版迁移需要重启；仅 profile/节点/探测配置变更使用热更新。当前没有 Vue 页面，已提供版本化本机 API 和 OpenAPI 定义。

## 本机部署（2026-10-16 17:10，Europe/Berlin）

已在 Ubuntu 20.04 / amd64、Python 3.8.10、systemd 245 的当前服务器上安装 GOST 3.3.0，启用 `gost.service` 开机自启，监听 `0.0.0.0:1080`，UDP 关闭。为此扩展系统白名单，并替换 Python 3.9 才提供的 `str.removeprefix`、`Path.is_relative_to`；发布包仍通过官方校验清单和仓库锁定 SHA-256 校验。

本机验证通过：10 项单元测试、Shell 语法、服务重启、有效账号认证、匿名/错误密码/未知账号拒绝、UDP 关闭，以及经 SOCKS5 服务端解析域名并转发 TCP。在回环地址和本机公网地址上均完成 TCP echo；经代理访问 `https://example.com/` 返回 HTTP 200。重复安装保持同一服务进程，配置权限为 `root:gost 0640`。不含密码的运行验证结果保存在本机 `test-results/deployment.json`。

尚未从外部客户端验证入站可达性，也未重启宿主机；开机启动已由 systemd 标记为 enabled。此次未重跑完整容器/ARM64 矩阵；已将 Ubuntu 20.04 加入后续集成测试与证据收集范围。以下记录和源码哈希属于初始版本测试，保留供参考。

## 初始版本验证

完成时间：2026-10-16 20:08（Asia/Shanghai）。默认版本为 GOST 3.3.0，版本切换场景同时使用官方 3.2.6 发布包。

## 最终结果

| 环境 | 验证内容 | 结果 |
| --- | --- | --- |
| 宿主 Python 3 | 8 项配置策略测试、Shell/Python 语法及空白检查 | 通过 |
| Ubuntu 22.04 / amd64 systemd 容器 | 17 项生命周期场景、Bash 安装入口、容器重启后自动运行 | 通过 |
| Ubuntu 24.04 / amd64 systemd 容器 | 同上 | 通过 |
| Debian 12 / amd64 systemd 容器 | 同上 | 通过 |
| Debian 13 / amd64 systemd 容器 | 同上 | 通过 |
| Ubuntu 24.04 / ARM64 GOST + QEMU | 3.3.0、3.2.6 各验证 UDP 关闭/开启，共 4 组认证、域名及 TCP/UDP 场景 | 通过 |
| Debian 12 / ARM64 GOST + QEMU | 同上 | 通过 |
| 官方 HTTPS 下载组件 | 通过宿主既有网络代理下载，校验锁定 SHA-256、解包、版本/架构及原生配置解析 | 通过 |

生命周期场景覆盖：

- 已有二进制、账号、unit 和外部 drop-in 的冲突保护，以及损坏发布包拒绝安装。
- 首次安装启动失败时撤销新服务、配置和账号；安装幂等执行不重启现有服务。
- 多用户、UTF-8/特殊字符密码、错误密码和匿名访问拒绝；客户端发送域名，由代理端解析并完成 TCP 转发。
- UDP 默认禁用，开启后进行真实 UDP echo 中继。
- start/stop/restart、异常退出自动重启、容器重新启动后的开机自启。
- 配置更新、配置回滚、端口占用保护、运行失败自动恢复，以及停止/禁用状态的保留。
- 真实 3.3.0 ↔ 3.2.6 二进制切换与回滚；下载失败保护；保留配置卸载、重新安装和完整清除。

启动失败场景注入无法运行的候选程序，实际服务启动、失败检测及恢复均由容器内 systemd 执行。下载失败采用故障注入验证变更前的保护路径。成功的完整服务安装使用校验后的离线官方发布包；联网下载单独进行组件验证。

## 源码与原始证据

最终一轮测试开始和结束时校验源码 SHA-256 一致，原始结果位于本地 `test-results/`：

- `evidence.json`：源码哈希、四个系统的场景结果、ARM64 模拟结果及日志哈希。
- `matrix.log`、各系统 `.log` / `.log.build`：完整矩阵执行与镜像构建记录。
- `static.log`、`online-download.log`：配置测试和联网下载组件验证记录。

这些生成文件已被 Git 忽略；重新执行 `bash tests/run-integration.sh` 可生成新一轮记录。仓库提供 GitHub Actions 工作流，本次交付未在远程 CI 执行。

本次关键源码 SHA-256：

```text
gostctl
8cc5a3201d6c40c5b5e723680b0b49cf9c932e8b821ffaeaacee06ebfbc7432a

install.sh
acb3bb0bd3e28465ad4c0e7fe95a4541cdc5a0df4e267c16b26952fabfb7bd81

tests/integration.py
43afb31f5b61cd88ab8bf9e4c533d77b92b921458f1a7e89d6f5e1dd5059fdbc

tests/run-integration.sh
6d45e1d90433f95e63f0d79d7b6ea809ce89ab9a02193382e1da3c15ec473bea
```

## 验证边界

systemd 验证使用共享宿主内核的隔离容器；ARM64 运行的是官方真实二进制，通过 QEMU 执行，尚未进行 ARM64 实机 systemd 或独立虚拟机验收。公网客户端、云安全组、防火墙和 NAT 下的 UDP 可达性由具体部署环境决定，本次未验证。

宿主机未安装 GOST 服务，收尾检查 `gost.service` 为 `not-found`。测试服务均在已清理的临时容器内运行。

## 故障定位

先对候选文件运行 `gostctl check --config ...`，再查看 `sudo gostctl status` 和 `sudo journalctl -u gost -n 100 --no-pager`。端口占用可用 `sudo ss -ltnp 'sport = :1080'` 定位；更改端口后同步调整客户端和网络规则。

本机连接正常、公网连接失败时，检查监听 IP、服务器防火墙及云安全组。TCP 正常而 UDP 失败时，检查实际分配的 UDP 中继端口及 NAT 返回地址。首次安装下载失败时检查 HTTPS 出口和 CA 证书，也可按 README 使用官方发布包离线安装。
