# 验证记录

## IPv6 支持（2026-10-23，Asia/Shanghai）

新生成配置在主机可绑定 `[::]` 时默认监听 `[::]:1080`（否则 `0.0.0.0:1080`），已安装配置不迁移。AnyTLS sing-box 实例与端口占用检查改用同一通配地址；Nginx 模板仅在主机支持 IPv6 时输出 `listen [::]`，所有权校验接受两种变体。管理访问地址接受 `[v6]` 并规范化为压缩小写形式（含旧记录读取时），保证与浏览器 `Host` 头一致；登录限流对 IPv6 按 /64 聚合，IPv4 映射地址归入 IPv4。安装脚本建议地址跳过链路本地 `fe80:`，并剥离方括号。

| 检查 | 本轮结果 |
| --- | --- |
| 静态检查 | Prettier、gofmt、go vet、Bash 语法、Git 空白检查逐项手动执行通过（本机 `make check` 因 `GOTOOLCHAIN=local` 与本地 Go 1.27.0 不符而无法直接运行，Go 1.27.2 经自动工具链使用） |
| Go 全量与 race | `go test ./internal/...`、`go test -race ./internal/...` 通过 |
| 真实 amd64 协议 | `GOST_TEST_REAL=1`：新增 `TestRealRoutingIPv6`（`[::1]` 入口、IPv6 SOCKS5 上游、IPv6 TCP 探测与目标）与原 `TestRealRouting` 通过；`TestAnyTLSProtocol` 以 `::` 监听、IPv4 客户端通过 |
| 前端 | 9 项单元测试、8 项模拟 API 浏览器测试通过（含 `[::]:1081` 入口编辑）；真实 systemd 浏览器用例跳过 |

边界：未运行 `make integration` 七环境矩阵；Docker 环境默认没有公网 IPv6，IPv6 仅在本机回环验证。未进行公网 AAAA、IPv6 防火墙/云安全组、Let's Encrypt IPv6 验证或内核禁用 IPv6 主机的实机验收（该分支仅由模板单元测试覆盖）。入口 UDP 经 IPv6 的转发未单独验证。源码未提交、未推送。

## 一键安装、密码登录与网页部署向导（2026-10-20，Asia/Shanghai）

安装脚本支持独立下载稳定发行包并校验，自动创建默认认证 SOCKS 配置、启动服务并提供自签名 HTTPS 初始化入口。一次性初始化码只保存摘要，管理员密码使用 PBKDF2；浏览器使用 Secure/HttpOnly 会话 Cookie、CSRF 与 Origin 校验，管理 Bearer 已移除。root CLI 使用双向校验 UID 的 Unix socket。网页支持 HTTPS/AnyTLS 后台部署任务、任务恢复、基础模式及密码修改；正式 HTTPS 就绪后五分钟关闭临时入口。新增 tag 发布 workflow，未创建 tag 或实际发布。

| 检查 | 结果与范围 |
| --- | --- |
| 最终源码静态检查、Go 全量测试 | `setup-check-final.log`、`setup-go-final.log` 通过；真实协议测试启用，未变更包允许 Go 缓存 |
| race | `setup-race.log` 全量通过；最后的只读 setup 路由限制及回归测试另由 `setup-api-final-race.log` 通过 |
| 前端 | Node.js 24 下 9 项单元测试、6 项模拟 API 浏览器测试通过；真实 systemd 浏览器流程 1 项通过并保存 trace |
| 隔离系统 | Ubuntu 20.04/22.04/24.04、Debian 12/13 的生命周期、独立安装脚本、初始化/密码/会话失效、真实协议、Nginx/Pebble、AnyTLS、续期及重启恢复通过 |
| ARM64 | Ubuntu 24.04、Debian 12 的 QEMU 8.2.2 真实 GOST/Mihomo/AnyTLS 协议通过；不代表 ARM64 硬件或 systemd 验收 |
| 发行包 | 最终源码重建 amd64/arm64 包，内外 SHA-256、静态 ELF、包内脚本/文档及 amd64 程序一致性通过 |

集成测试基于 `source-baseline.json` 中的源码快照，绑定产物和各环境日志的记录是 `test-results/setup-integration-evidence.json`。七环境通过后收尾版本与该快照的差异仅为 `internal/api/setup.go` 拒绝对只读 setup 路由写入，以及新增 `internal/api/setup_test.go` 回归测试；最终版本重新执行 Go 全量、API race、静态检查和发行包构建。**没有将该最后修改宣称为完整七环境重新验收。** 最终源码、专项检查与发行包摘要见 `setup-final-evidence.json`。

这不是一次无失败的连续矩阵：早期严格 umask 导致内核缺少执行权限的问题已修复并增加回归；早期 QEMU AnyTLS 曾偶发连接重置，单独三次诊断与后续正式两环境通过，但根因未确认。最终集成快照的 Ubuntu 24 首次因 APT 下载失败退出，第一次补跑在浏览器切换出口时报告连接异常；再次开启 trace 后整条流程通过，期间没有修改这条业务路径，也未放宽断言。浏览器瞬时异常根因仍未确认。失败日志分别保留在 `setup-attempt1/`、`setup-attempt2/`、`setup-attempt3-*.log`；`setup-matrix.log` 原退出失败不改写为成功，Ubuntu 24 成功补跑见 `setup-ubuntu24-trace.log`。

网络下载使用获准的 `127.0.0.1:7890` 代理；隔离容器经临时 Docker bridge 转接访问。未修改宿主代理或 APT 设置。测试使用 Pebble CA，没有进行公网 Let's Encrypt/DNS/防火墙验证；GitHub Actions 仅完成本地 YAML 和内嵌 Bash 校验，没有远端运行或发布。所有源码保持未提交、未推送。

## RelayHarbor 重命名验证（2026-10-20，Asia/Shanghai）

本地仓库迁移至 `/home/dsh/Desktop/relayharbor`，Go 模块统一为 `github.com/ShaohongDong/relayharbor`，`origin` 拉取与推送地址均为 `git@github.com:ShaohongDong/relayharbor.git`。项目文档、OpenAPI 标题、Vue 页面、前端包名及发行包名称同步更新；Git 历史保留，未提交或推送。`gostctl`、systemd 服务名、安装/状态目录、配置与 API 契约均保持原样。生产 Go 文件的改动逐项核对为模块 import 替换。

| 检查 | 本轮结果 |
| --- | --- |
| Node.js 24.22.0 / npm 11.19.1、Go 1.27.2 | 前端生产构建、TypeScript、Prettier、gofmt、go vet、Bash 与 Git 空白检查通过 |
| Go 单元与真实 amd64 协议 | `GOST_TEST_REAL=1 go test -count=1 ./...` 通过，包含现有 GOST 与 Mihomo/AnyTLS 协议测试 |
| 前端与浏览器 | Node.js 24 下 9 项单元测试、5 项受控 API 浏览器测试通过；浏览器使用本机 Chrome |
| amd64 / arm64 发行包 | 新名称归档、内部目录、静态 ELF、包及程序 SHA-256 验证通过；amd64 包内程序与本轮构建一致 |
| CLI 与仓库 | `gostctl version/init/check` 通过；原 HEAD 保留，旧目录无残留或软链接，origin 地址核对一致 |

发行文件名为 `relayharbor_0.1.0-dev_linux_<arch>.tar.gz`，校验清单仅收录当前版本的新名称发行包，旧缓存未删除。先使用宿主 Node.js 26 完成初步检查，随后通过官方 HTTPS 下载并校验 Node.js 24 归档，在仓库缓存内补齐规定版本并重做前端构建与测试，未修改宿主默认 Node.js。

本轮日志为 `test-results/rename-*.log`，源码、产物和日志哈希见 `test-results/rename-evidence.json`。未重跑完整 race、systemd 容器矩阵、真实 systemd 浏览器链路或 ARM64 QEMU；未验证 SSH 写入权限或修改已安装服务。以下章节及原 `evidence.json` 为重命名前的历史验收，不能作为本轮源码的完整系统验收。

## AnyTLS 多用户与 Clash/Mihomo 订阅验收（2026-10-20，Asia/Shanghai）

本轮新增独立用户/设备 AnyTLS 入口、HTTPS 完整订阅及 proxy-provider 节点列表、CLI/API/Vue 用户管理。使用官方固定版本 sing-box 1.14.3；真实客户端为 Mihomo 1.19.32，两架构归档均校验仓库锁定 SHA-256。用户实例以非特权 gost 账号运行，单个用户变更只操作对应 systemd 服务，所有实例通过认证 SOCKS5 进入原调度器。

最终完整矩阵通过，报告生成时间为 2026-10-20T08:03:57Z。构建前后源码哈希一致，报告绑定 72 个源码文件、11 个管理器/测试/工具二进制、4 个前端资源和 9 份环境回执；结束后重新计算全部哈希并确认一致。

| 检查 | 内容 | 当前结果 |
| --- | --- | --- |
| 静态检查 | TypeScript、Prettier、gofmt、go vet、Bash 语法及空白检查 | 通过 |
| Go 行为与真实协议 | 独立凭据、撤销、失败恢复、资源归属、缓存符号链接拒绝；真实 Mihomo TCP、证书校验、错误密码与 UDP 拒绝 | 通过 |
| Vue / 浏览器 | 9 项前端单元测试；5 项浏览器测试，包括 AnyTLS 独立 revision、显式凭据读取、编辑保护及冲突处理 | 通过 |
| Ubuntu 24.04 定向 systemd 联调 | Nginx 订阅、双用户隔离、撤销凭据、混合代理池故障切换与全池失败、证书热加载及重启恢复 | 通过 |
| ARM64 定向协议 | QEMU 8.2.2 下真实 ARM64 GOST、sing-box 与 Mihomo 互通 | 通过 |
| 完整 Go race | `go test -race ./...` | 通过 |
| 完整系统矩阵 | Ubuntu 20.04/22.04/24.04、Debian 12/13 systemd，Ubuntu 24.04/Debian 12 ARM64 QEMU，真实浏览器管理链路 | 全部通过 |
| amd64 / arm64 发行包 | 静态 Go 管理器、包完整性校验 | 通过 |

定向联调保持用户 B 的 Mihomo TCP 长连接，同时对 A 执行订阅 token 轮换、密码轮换、停用、启用及删除；B 的连接及实例 PID 保持。旧订阅 token 返回 404；密码轮换关闭 A 的旧连接。通过实际订阅驱动客户端，验证代理池轮询/故障切换、全池失败不回退直连，以及 profile 切换不影响已有流。

证书测试使用隔离 Pebble ACME 与真实 HTTP-01，客户端校验测试 CA 和域名；生产 systemd 续期任务更新 Nginx 和 AnyTLS 证书。每个活跃实例的实际 TLS 叶证书须匹配发布版本，原 Mihomo 流保持且 PID 不变。单位测试注入证书热加载失败并验证旧证书恢复。一次 Ubuntu 24.04 双用户快照的 sing-box RSS 分别约 57–59 MiB；这是测试时点记录，不是容量或性能承诺。

开发阶段失败记录保留在忽略的 `test-results/anytls-dev*.log`、`anytls-arm64-dev*.log`，不作为成功回执：补齐 systemd 的 AF_NETLINK 地址族、测试客户端保留域名解析、容器内 fixture 路径，以及启动测试前等待 systemd 完成初始化。宿主 QEMU 6.2 运行官方 ARM64 sing-box 时出现缓冲池类型异常；改用从 Ubuntu 官方签名软件源提取的 QEMU 8.2.2 后定向协议检查通过，未改动生产内核、关闭 TLS 验证或放宽业务断言。ARM64 测试镜像提供对应 glibc，模拟器文件纳入最终证据哈希。

下载使用用户授权的本机 127.0.0.1:7890 代理；容器经 Docker bridge 上的临时转接使用该代理。APT 仍使用官方软件源和签名检查，未修改宿主 APT 或生产代理配置。测试容器与临时转接均已清理。

本轮先完成 Ubuntu 20.04/22.04，随后按用户要求并行执行剩余三个 systemd 环境及两组 ARM64 QEMU。使用固定构建产物和独立容器/日志；Ubuntu 24.04 从已完成的协议检查继续执行。原串行编排被主动终止，其退出状态不作为测试失败或最终成功回执。最终并行编排的 `test-results/anytls-parallel-final.exit` 为 0，执行记录为 `anytls-parallel-execution.json`，统一证据为 `evidence.json`。

其他原始记录位于 Git 忽略的 `test-results/`：`anytls-final-check.log`、`anytls-final-race.log`、`anytls-final-web-test.log`、`anytls-final-browser.log`、`browser-real.log`、`browser-systemd.log`、各系统/ARM64 日志，以及 `anytls-release*.log`。发行包位于 `dist/`，两架构均为静态 Go 管理器；amd64 包内程序与本轮验收程序逐字节相同。

边界：未进行公网 Let's Encrypt 签发、公网 DNS/防火墙/NAT 验收、ARM64 实机 systemd 验收或远程 CI。AnyTLS 首版为 IPv4 TCP 入口，不导入外部订阅、不提供 UDP、计费、配额或分用户路由；目标域名需有可达的 A 记录。源码与忽略的测试证据在本地保留，未提交或推送。

## 历史：Nginx HTTPS 与自动证书验收（2026-10-20，Asia/Shanghai）

本轮新增专用服务器的 Nginx 反向代理、Let's Encrypt HTTP-01 签发与自动续期，以及 Vue HTTPS 状态、检查续期和重载操作。使用固定版本 lego 5.5.2，并同时验证官方校验清单与仓库锁定 SHA-256；生产 Go 代码继续只依赖标准库，目标机无需 Node.js、Go 或 Python。

最终完整矩阵通过，报告生成时间为 2026-10-20T06:59:20Z。构建前后源码哈希一致，报告绑定 60 个源码文件、8 个管理器/测试/工具二进制、4 个前端资源和 9 份环境回执。

| 检查 | 内容 | 结果 |
| --- | --- | --- |
| 静态与前端单元测试 | TypeScript、Prettier、生产构建、gofmt、go vet、Bash 语法、空白检查及 9 项前端测试 | 通过 |
| 浏览器交互 | 4 项受控 API 浏览器测试，新增 HTTPS 配置提示、后台任务与失败状态 | 全部通过 |
| Go race | 完整 `go test -race ./...`，含证书失败恢复、并发操作、受管资源与发布包校验测试 | 通过 |
| Ubuntu 20.04 / 22.04 / 24.04、Debian 12 / 13 | 完整生命周期、真实转发、Nginx 与本地 ACME 签发/续期、HTTPS 和重启恢复 | 全部通过 |
| Ubuntu 24.04 真实浏览器链路 | 内置 Vue → Go API → systemd → 真实 GOST 的管理与连接保持 | 通过 |
| Ubuntu 24.04 / Debian 12 ARM64 QEMU | 真实 ARM64 GOST 转发、管理器及 lego 5.5.2 执行 | 全部通过 |
| 两架构发行包 | 静态 ELF、包 SHA-256；amd64 包内程序与本轮构建逐字节相同 | 通过 |

证书集成测试使用固定镜像中的 Pebble 2.10.1，通过真实 Nginx 提供 HTTP-01 验证文件；HTTPS 客户端验证本地 CA 链与域名。短有效期证书用于触发真实续期，并由生产 systemd oneshot 执行。测试还检查持久化 timer 的重启恢复、HTTPS 下的认证与 revision 保护、续期期间既有 SOCKS 连接和服务 PID 保持、停用与重新启用、严格 umask 下的公开验证目录，以及首次包管理失败后的公开卸载清理与重新安装。

故障测试覆盖签发失败重试、Nginx 重载失败保留旧证书、已签发证书优先重试部署、域名切换回滚、过期候选证书不阻止续期、外部 Nginx/配置/drop-in 拒绝接管，以及符号链接和被篡改的客户端缓存拒绝执行。

原始记录位于 Git 忽略的 `test-results/`：`nginx-matrix-final.log`、各系统及 ARM64 日志、`browser-mock.log`、`browser-real.log`、`browser-systemd.log`、`nginx-static.log`、`nginx-race.log`、`nginx-release.log` 和 `nginx-release-checksums.log`。`nginx-matrix-final.exit` 为 0；`evidence.json` 绑定源码、程序、前端资源与环境回执。下列章节属于历史检查点。

开发期间保留了浏览器缺失、Debian HTTP 镜像连接停滞及用户中断的失败或未完成记录，不作为通过证据。本机浏览器检查使用现有 Google Chrome；Debian 测试镜像改用同一官方仓库的 HTTPS 地址。权限测试比较外部目录操作前后的实际权限，不依赖临时目录固定为某一权限。

本轮按用户提供的 `127.0.0.1:7890` 代理加速 Debian 下载：通过仅监听 Docker 网桥的临时 TCP 转接，为 Debian 12/13 测试容器配置 APT 代理。Debian 12 已开始的索引下载保持直连，后续安装及 Debian 13 下载使用代理。未关闭 HTTPS 或包签名校验，未修改生产部署的 APT 配置；测试容器与临时转接均已清理。

验证边界：本轮未向公网 Let's Encrypt 申请证书，未验证真实域名解析、公网入站、防火墙或 NAT；本地 ACME 成功不能替代实际部署可达性验证。systemd 容器共享宿主内核，ARM64 QEMU 不构成实机 systemd 验收。未在宿主机安装 Nginx/GOST，未执行远程 CI，未提交或发布。

## Vue 单页面控制台验收（历史检查点，2026-10-20，Asia/Shanghai）

本轮新增中文、固定浅色的 Vue 单页面控制台：代理池与节点紧凑列表、入口认证编辑、脱敏配置预览和服务运维。前端采用 Vue 3 / TypeScript / Vite / Element Plus，构建资源通过 Go embed 随静态程序交付。部署端不增加 Node.js、npm、Go 或 Python 运行依赖。API 保持回环监听与 Bearer 认证，新增 revision 保护的启停重启、回滚和有界日志接口。

最终完整矩阵通过；构建前后源码哈希一致，报告绑定 53 个源码文件、6 个测试/管理器二进制、4 个前端资源，以及 7 个系统/模拟环境和 2 份真实浏览器/容器回执。报告生成时间为 2026-10-20T04:56:59Z。以下 Go 与 Python 章节均为历史检查点，不替代本轮证据。

| 检查 | 内容 | 结果 |
| --- | --- | --- |
| 前端静态与单元测试 | TypeScript、Prettier、生产构建；9 项配置草稿、密码保留/脱敏、身份变化、配置转换及 API 请求测试 | 通过 |
| 浏览器交互 | 3 项真实浏览器测试使用受控 API 回应，验证节点编辑、状态刷新不覆盖草稿、revision 冲突保留草稿、启停、纯文本日志和窄屏布局 | 全部通过 |
| Ubuntu 24.04 真实浏览器链路 | 浏览器 → 测试隧道 → Go API → systemd → 经过校验的真实 GOST；认证失败/成功、代理池创建、认证 HTTP 节点添加、热更新/切换、探测、入口修改重启、启停重启、回滚与日志读取 | 通过 |
| 连接与服务独立性 | 热切换和路由配置应用保持既有 TCP 流、GOST 入口 PID 与 supervisor PID；停止转发后仍可通过页面启动；整个浏览器会话 API PID 不变 | 通过 |
| Go race / 静态检查 | go test -race ./...、gofmt、go vet、Bash 语法与 Git 空白检查 | 通过 |
| Ubuntu 20.04 / 22.04 / 24.04、Debian 12 / 13 | 每环境执行完整生命周期、真实转发、启动恢复，以及内置页面、新增启停重启/回滚/日志 API、revision 保护检查 | 全部通过 |
| Ubuntu 24.04 / Debian 12 ARM64 QEMU | 每环境运行 Go 调度器与真实 ARM64 GOST 的 9 项转发场景，以及 ARM64 管理器版本命令 | 全部通过 |
| 发行包与依赖 | 两架构静态 ELF、发行包与包内程序校验；amd64 发行包程序与验收管理器逐字节相同；五个目标镜像均无 Node.js/npm/Go/Python | 通过 |

本机验证工具为 Go 1.27.2、Node.js 26.3.0、npm 12.2.0、Google Chrome 152.0.7977.82。依赖锁定在 npm lockfile；CI 已配置 Node.js 24 和 Playwright 自带 Chromium，本轮未执行远程 CI。宿主 Chromium 下载遇到网络错误后，使用现有 Google Chrome 完成全部本地浏览器检查。桌面和窄屏截图位于 test-results/console-desktop.png、test-results/console-real-mobile.png。

原始证据位于 Git 忽略的 test-results/：evidence.json、vue-matrix.log、各系统日志、browser-mock.log、browser-real.log、browser-systemd.log、vue-static.log、vue-race.log、vue-release.log、vue-no-runtime-deps.log。管理器 SHA-256：9cfaf2bccb4ac6b603805ac4762edce8048f3ac4fc58e58e587cf8e6f01407e8。静态资源哈希与二进制、日志哈希均记录在 evidence.json；发行包位于 dist/。

边界：systemd 容器共享宿主内核，ARM64 QEMU 不构成实机 systemd 验收；外部代理、公网入站及防火墙/NAT 未纳入本地回环检查。浏览器通过仅发布到宿主回环地址的测试桥接模拟 SSH 隧道，生产程序不包含桥接。日志不开放 worker 原始文件，仅屏蔽已知受管凭据与认证头，不承诺识别历史任意文本中的未知秘密。未在宿主机安装/升级服务，未提交、发布或推送。

## Go 重构验收（历史检查点，2026-10-20，Asia/Shanghai）

当前版本的生产程序、单元测试、协议辅助、集成测试、下载与证据工具均已改为 Go，构建工具链为 Go 1.27.2。程序只依赖标准库，以 `CGO_ENABLED=0` 构建 Linux amd64/arm64 静态可执行文件，继续调用经过校验的独立 GOST。只支持 v2 管理配置和新的 Go 安装状态，不迁移 Python 安装，也不导入旧原生配置。后续章节属于历史实现记录。

最终完整矩阵通过，源码在构建前与验收结束时的 SHA-256 一致；报告同时记录测试程序、管理器二进制及环境日志哈希。

| 环境/检查 | 验证内容 | 结果 |
| --- | --- | --- |
| 宿主 Go 1.27.2 | 配置策略、严格 JSON/Unicode、凭据脱敏与保留、API/ETag、并发更新、事务恢复、退出等待及 OpenAPI 检查 | 通过 |
| Go race 检查 | `go test -race ./...`，含真实 GOST 转发与并发控制 | 通过 |
| 静态检查 | gofmt、go vet、Bash 语法、Git 空白检查 | 通过 |
| Ubuntu 20.04、22.04、24.04 / amd64 systemd 容器 | 生命周期、真实转发、安装脚本和容器重启后的 TCP/UDP | 全部通过 |
| Debian 12、13 / amd64 systemd 容器 | 同上 | 全部通过 |
| Ubuntu 24.04、Debian 12 / ARM64 QEMU | 每环境运行 Go 调度器、真实 ARM64 GOST 的 9 项转发场景及 ARM64 管理器版本命令 | 全部通过 |
| 两架构发行包 | 静态 ELF 构建、发行包及包内程序 SHA-256 校验 | 通过 |
| 五个系统测试镜像 | 确认没有安装 Go 或 Python，直接运行预编译程序和测试 | 全部通过 |

真实转发覆盖直连 TCP/域名和 UDP、认证 HTTP/SOCKS5 混合轮询、无认证 HTTP 上游、建连失败切换、全池故障拒绝、冷却与周期恢复、HTTP 状态码拒绝、切换确认与既有连接保持、无效候选保护、worker 重启及旧 worker 排空。

生命周期覆盖外部资源冲突保护、校验失败、首次启动失败清理、幂等安装、特殊凭据及强制认证、启停与异常重启、配置/版本更新和回滚、停止/禁用状态保留、下载失败保护、外部 drop-in 保护、Go 管理器替换及候选管理器启动失败自动恢复、保留配置卸载和完整清除。版本场景使用真实 GOST 3.3.0/3.2.6。恢复流程会等待旧 API 实际监听，正常停止与入口故障退出分别处理。

本轮原始记录位于 Git 忽略的 `test-results/`：`evidence.json`、`go-matrix.log`、各系统及 ARM64 日志、`go-race.log`、`go-static.log`、`go-release.log` 和 `go-no-runtime-deps.log`。可执行 `make fetch`、`make test-race`、`make integration` 重新生成结果；发行包位于 `dist/`。本轮未在宿主机安装服务，未发布、提交或推送，也未运行远程 CI。

边界：systemd 容器共享宿主内核，容器重启不等同于宿主重启；ARM64 QEMU 不等同于 ARM64 实机 systemd 验收。外部真实代理、公网入站、防火墙和 NAT 可达性未纳入本地回环场景。监听地址、入口凭据、UDP 开关、GOST 或管理器程序变化需要重启；profile/节点/探测配置热更新保留既有连接。没有 Vue 页面，保留原管理 API 契约。

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
