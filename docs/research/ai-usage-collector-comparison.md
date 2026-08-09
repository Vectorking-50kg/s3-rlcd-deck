# Codex / Cursor 用量采集方案对比

> 调研日期：2026-08-09  
> 目标平台：macOS 开发、Windows 常驻部署、ESP32-S3-RLCD-DECK 作为只读显示与配置终端  
> 证据范围：项目 README、固定提交源码、发布页、许可证与官方文档；不采用博客或二手测评作为结论依据。

## 1. 结论摘要

目前没有一个可信的开源项目能够同时完整满足以下要求：

- macOS 与 Windows 双平台；
- Codex 和 Cursor 个人账户用量；
- Codex 配额、历史 token 与任意既有 Codex CLI、Desktop、IDE 会话的精确实时状态；
- 凭证只读、不修改上游应用认证文件；
- 可直接安全暴露给局域网内的 ESP32；
- 同时可扩展 AIHubMix、DeepSeek 等余额接口。

最合适的实现不是原样部署某个项目，而是采用分层 Companion：

1. 以 **OpenUsage 的 Go 采集模块和跨平台单文件部署方式**为主体；
2. Codex 配额优先调用官方 App Server，而不是依赖私有 wham 接口；
3. 对 Companion 自己管理的 Codex 线程，使用官方 App Server 事件提供“已验证”状态；
4. 对已由 Desktop、CLI 或 IDE 独立启动的 Codex 会话，参考 **abtop** 以进程和 JSONL 旁路推断，并明确标记为“推断”；
5. 参考 **Token Monitor** 的局域网鉴权、第三方余额适配器与数据契约；
6. 参考 **Tokscale** 校验历史用量解析，并沿用其“不改写上游认证文件”的边界；
7. TokenTracker 只作为多平台打包、提供商适配和 UI 参考，不作为安全与采集底座。

这一组合比直接采用 TokenTracker 更符合“电脑保存凭证、ESP32 只接收脱敏聚合数据”的安全边界。该路线已于 2026-08-09 经需求确认，纳入本项目开发基线。

## 2. 官方能力边界

### 2.1 Codex：配额与精确状态有官方接口，但存在进程所有权边界

Codex App Server 当前使用 JSONL RPC，默认通过标准输入输出通信；WebSocket 仍标为实验性。官方协议提供：

- account/rateLimits/read 与 account/rateLimits/updated：账户速率限制；
- account/usage/read：日维度和生命周期 token 用量；
- thread/list、thread/read、thread/loaded/list：线程及其运行状态；
- thread/status/changed：状态变化，包括 activeFlags 中的 waitingOnApproval；
- turn/started、turn/completed、审批请求、用户输入请求等实时事件。

这些能力均可在 [Codex App Server 官方手册](https://learn.chatgpt.com/docs/app-server) 中核验。

需要特别限定“精确状态”的含义：App Server 事件能精确描述该 App Server 已加载、创建或恢复的线程。根据协议的连接和事件订阅模型可以推断，监控程序另行启动一个 App Server，并不能自动观察另一个 Codex Desktop、CLI 或 IDE 进程已经拥有的全部线程。因此：

- Companion 自己管理或订阅到的线程：可标记为 VERIFIED；
- 独立进程中的既有会话：只能旁路推断，不能宣称精确；
- v1 若不接管 Codex 工作流，大多数“正在进行的会话”只能提供 INFERRED 状态。

### 2.2 Cursor：个人用量没有公开、稳定的官方 API

Cursor 官方 API 文档将 Admin、Analytics、AI Code Tracking API 归入 Enterprise 团队能力；Cloud Agents 与 SDK 面向代理工作流，并不是个人订阅余额接口。[Cursor API 官方文档](https://cursor.com/docs/api.md)

Cursor 官方价格与模型文档只说明个人用户可在编辑器设置或网页 Usage Dashboard 查看用量，没有记录可供个人账户使用的公开用量 API。[Cursor Models & Pricing 官方文档](https://cursor.com/docs/models-and-pricing.md)

因此，当前所有个人 Cursor 用量采集项目都至少依赖以下一种非公开实现：

- 从 Cursor 的 state.vscdb 读取 access token；
- 调用 cursor.com 或 api2.cursor.sh 的私有接口；
- 解析本地 cursorDiskKV、ai-tracking 等内部存储。

这些能力必须在产品中标为 EXPERIMENTAL，并允许随 Cursor 升级失效；不能把某个私有接口包装成“官方保证”。

## 3. 评估口径

本报告按以下维度评估：

| 维度 | 关注点 |
|---|---|
| 双平台 | macOS 与 Windows 是否都有实际路径处理、构建或发布包 |
| Codex 用量 | 历史 token、账户使用量、5 小时／周额度、重置时间 |
| 会话状态 | 是否能区分正在执行、等待审批、等待输入、结束；是官方状态还是推断 |
| Cursor 个人用量 | 数据来源、是否依赖私有接口、能否读取本地会话数据 |
| 凭证行为 | 只读、复制保存、刷新、回写上游认证文件 |
| 网络面 | 是否已有 HTTP API、鉴权、绑定策略、请求上限、超时 |
| 可维护性 | 最近提交、发布、测试、语言与打包 |
| 许可证 | 是否适合直接或部分复用 |
| ESP32 适配 | 能否形成小而稳定、脱敏、可配对的数据接口 |

会话状态分为三级：

- **VERIFIED**：来自官方 App Server 对该连接所管理线程的事件；
- **INFERRED**：由进程、JSONL 修改时间和日志内容推断；
- **UNAVAILABLE**：无可信状态来源。

## 4. 主要跨平台候选对比

| 项目 | macOS + Windows | Codex 用量／额度 | Codex 活动状态 | Cursor 个人用量 | 凭证行为 | 网络与打包 | 适合作为 |
|---|---|---|---|---|---|---|---|
| OpenUsage | 是；Go 单文件，多平台 release | JSONL、私有 wham、App Server | 只有最近会话信息，非精确事件状态 | 私有 Connect RPC + 本地只读数据库 | 未发现刷新或回写上游凭证 | 有 Hub、Bearer、限流边界；但原始快照过宽 | **首选采集主体，需加安全 DTO** |
| Token Monitor | 是；Electron，多平台安装包 | App Server 结构化额度，另有本地数据 | clientStatus 是采集器状态，不是 Codex 代理状态 | 经 Tokscale／私有接口 | Cursor token 复制到自有文件 | Hub 鉴权、SSE、第三方适配较完整 | **首选架构与契约参考** |
| Tokscale | 是；Rust 原生 CLI，多平台二进制 | 历史聚合、账户 activity；无实时线程状态 | 不支持 | 私有接口，本地 token 可复制到自有文件 | 明确不改写提供商拥有的 Codex 凭证 | JSON 输出，无常驻 HTTP 服务 | **解析与安全策略参考** |
| TokenTracker | 是；Node/Electron 风格打包 | JSONL + 私有 wham | 无官方活动状态；历史扫描可能跳过活跃文件 | 私有 Cursor API + SQLite token | **会刷新并原子回写 Codex auth.json** | 仅默认回环 Web；无适合 ESP32 的配对 API | UI／兼容参考，不宜做底座 |
| caut | README 声称支持 | 代码仅实装 Claude／Codex 主模块 | 不支持 | Cursor 实现不完整 | 不构成可用基线 | 无成熟 release | 排除 |

### 4.1 活跃度与发布快照

活跃度只表示项目仍在维护，不代表私有 API 稳定或安全边界符合本项目要求。

| 项目 | 核验提交日期 | 核验 release | 维护判断 |
|---|---:|---:|---|
| OpenUsage | [2026-08-08](https://github.com/janekbaraniewski/openusage/commit/dda58e29326ac8cd63c4860197dfa0b64359a972) | v0.24.1，2026-08-01 | 活跃，迭代较快 |
| Token Monitor | [2026-08-09](https://github.com/Javis603/token-monitor/commit/3944fd5c2db124a94deb49eeaf82284f8bae7eaf) | v0.42.1，2026-08-08 | 活跃，发布与源码同步 |
| Tokscale | [2026-08-09](https://github.com/junhoyeo/tokscale/commit/965c59e68d8826714036c2abad2298b2efd245fd) | v4.12.0，2026-08-08 | 活跃，原生包覆盖完整 |
| TokenTracker | [2026-08-09](https://github.com/xiufengsun/TokenTracker/commit/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec) | v0.88.3，2026-08-09 | 活跃，但变化速度也放大兼容风险 |
| caut | [2026-06-24](https://github.com/Dicklesworthstone/coding_agent_usage_tracker/commit/d6dc2394dfba511a983a20dcc06b6a81e802ca7e) | 无成熟 release | 可见维护，但 provider 完整度不足 |

## 5. 候选项目详评

### 5.1 OpenUsage：最强的综合采集主体，但不能原样暴露 Hub

项目： [janekbaraniewski/openusage](https://github.com/janekbaraniewski/openusage/tree/dda58e29326ac8cd63c4860197dfa0b64359a972)  
许可证： [MIT](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/LICENSE)  
核验版本：提交 dda58e2；[v0.24.1](https://github.com/janekbaraniewski/openusage/releases/tag/v0.24.1)

**优势**

- README 明确支持 Codex、Cursor、DeepSeek 等提供商，支持 headless JSON、daemon 与 Hub，适合做电脑侧常驻 Companion。[README：提供商、运行模式与 Hub](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/README.md#L37-L79)
- release 同时提供 macOS amd64／arm64、Linux 和 Windows amd64 二进制，Go 单文件比 Electron 更适合 Windows 常驻部署。[README：安装与二进制](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/README.md#L198-L219)
- Cursor 检测代码按平台定位 state.vscdb，并以 mode=ro 打开，只读取 cursorAuth/accessToken、邮箱与会员信息；未发现读取 refresh token、刷新或写回 Cursor 数据库的路径。[Cursor 检测源码](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/internal/detect/cursor.go#L65-L112)
- Cursor telemetry 同样以只读方式解析 ai-tracking、cursorDiskKV 和 Composer 记录，可取得会话 ID、模型、token 及内部状态字段。[Cursor telemetry 源码](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/internal/providers/cursor/telemetry.go#L74-L125) [Cursor state 解析源码](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/internal/providers/cursor/state_records.go#L89-L139)
- Hub 已有 Bearer 校验、请求体上限、读写超时和推送／快照接口；非回环绑定时若没有认证会拒绝启动。[Hub 服务源码](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/internal/hub/server.go#L18-L108) [Hub 启动策略](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/cmd/openusage/hub.go#L68-L129)

**局限与风险**

- Codex 文档同时使用本地 JSONL、私有 chatgpt.com/backend-api/wham/usage 和 App Server。私有接口应降级为 fallback，官方 App Server 应成为首选配额通道。[Codex provider 文档](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/docs/site/docs/providers/codex.md#L55-L75)
- Codex 会话部分主要依据最新 JSONL 和修改时间，不提供 waitingOnApproval、等待用户输入等官方线程事件，不能满足“精确显示任意现有 Codex 会话”的要求。[Codex provider 实现](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/internal/providers/codex/codex.go#L281-L303)
- Cursor 的 status 来自 Cursor 内部本地存储，不等于经过公开协议定义的实时代理状态，应视为实验性提示。
- OpenUsage 的公开 UsageSnapshot 类型包含 Attributes、Diagnostics、Raw、ModelUsage 和 DailySeries。[快照类型](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/internal/core/types.go#L47-L60)
- 普通导出会主动剥离 Raw，因为 provider probe 可能在 Raw 中放入凭证提示；但远程 exporter 与 Hub 快照仍传递完整 UsageSnapshot。[安全导出逻辑](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/internal/export/encode.go#L133-L149) [远程推送源码](https://github.com/janekbaraniewski/openusage/blob/dda58e29326ac8cd63c4860197dfa0b64359a972/internal/exporter/exporter.go#L127-L151)
- Codex Raw 数据可能含账户邮箱，Hub 健康信息还会包含机器名。因此不能让 ESP32 直接消费现有 /v1/snapshots。

**复用建议**

复用 provider、detect、平台路径、daemon 和构建体系；新增独立的 device API：

- 只返回显式 allowlist DTO；
- 删除 Raw、Attributes、Diagnostics、邮箱、绝对路径、机器名、prompt、聊天内容和工具参数；
- 会话标题默认隐藏，只有用户明确开启才发送；
- Codex 配额改为 App Server first；
- 非回环监听必须先完成设备配对。

综合评价：**A-，最适合作为主体，但必须修改接口与 Codex 优先级。**

### 5.2 Token Monitor：局域网与第三方余额契约的最佳参考

项目： [Javis603/token-monitor](https://github.com/Javis603/token-monitor/tree/3944fd5c2db124a94deb49eeaf82284f8bae7eaf)  
许可证： [MIT](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/LICENSE)  
核验版本：提交 3944fd5；[v0.42.1](https://github.com/Javis603/token-monitor/releases/tag/v0.42.1)

**优势**

- 同时支持 Codex、Cursor 和第三方余额源，并提供 macOS、Windows、Linux 安装包。[README：数据源与打包](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/README.md#L27-L64) [README：平台包](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/README.md#L143-L164)
- Codex 额度采集器会跨平台发现 app-server，调用 account/rateLimits/read 与 account/read，然后主动关闭子进程。这比直接依赖 wham 更稳定，也更符合官方边界。[App Server 采集源码](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/src/shared/limitCollector.js#L2626-L2824)
- API 使用共享密钥 Bearer；文档明确 raw credentials、原始日志、prompt 与代码不会发送到 Hub。[API 鉴权](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/docs/API.md#L1-L20) [隐私说明](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/docs/privacy.md#L1-L25)
- 第三方 provider 支持 Bearer 或 x-api-key、自定义 GET 地址和 JSON path 映射，适合借鉴为 AIHubMix、DeepSeek 的“curl 式余额适配器”。[第三方接口契约](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/docs/API.md#L284-L295)

**局限与风险**

- 文档中的 clientStatus active、waiting、missing 描述的是采集客户端是否在线／是否上传，不是 Codex agent 正在执行还是等待审批。[clientStatus 语义](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/docs/API.md#L215-L282)
- Cursor 依赖 Tokscale 或私有 cursor.com 接口。token 被复制保存到自有 cursor-credentials.json，而不是始终从上游只读获取；非 Windows 上会 chmod 0600，但 Windows 不具备同等 POSIX 权限语义。[Cursor credential 源码](https://github.com/Javis603/token-monitor/blob/3944fd5c2db124a94deb49eeaf82284f8bae7eaf/src/shared/cursorAuth.js#L22-L89)
- Electron 运行时和完整 UI 对本项目的 Companion 过重。

**复用建议**

借鉴 Hub 鉴权、SSE、第三方余额 schema、错误状态与隐私声明；不要整包嵌入，也不要把采集器在线状态误显示为 Codex 会话状态。

综合评价：**A（架构参考），B（作为直接运行底座）。**

### 5.3 Tokscale：成熟的历史聚合和安全边界参考

项目： [junhoyeo/tokscale](https://github.com/junhoyeo/tokscale/tree/965c59e68d8826714036c2abad2298b2efd245fd)  
许可证： [MIT](https://github.com/junhoyeo/tokscale/blob/965c59e68d8826714036c2abad2298b2efd245fd/LICENSE)  
核验版本：提交 965c59e；[v4.12.0](https://github.com/junhoyeo/tokscale/releases/tag/v4.12.0)

**优势**

- Rust 原生 CLI，有 macOS x64／arm64、Windows x64／arm64 和 Linux 包，支持 JSON 输出，适合做自动化采集或回归校验。[README：平台与 JSON](https://github.com/junhoyeo/tokscale/blob/965c59e68d8826714036c2abad2298b2efd245fd/README.md#L244-L273)
- 能解析 Codex sessions 与 Cursor 用量，已有较成熟的日／月／模型聚合。[README：provider 数据位置](https://github.com/junhoyeo/tokscale/blob/965c59e68d8826714036c2abad2298b2efd245fd/README.md#L52-L105)
- Codex usage 路径有明确安全注释：不能改写 provider-owned auth；只有 Tokscale 自己拥有的账户存储可刷新。上游凭证被拒绝时直接失败，不在后台偷偷旋转。[Codex credential policy](https://github.com/junhoyeo/tokscale/blob/965c59e68d8826714036c2abad2298b2efd245fd/crates/tokscale-cli/src/commands/usage/codex.rs#L565-L600) [刷新边界](https://github.com/junhoyeo/tokscale/blob/965c59e68d8826714036c2abad2298b2efd245fd/crates/tokscale-cli/src/commands/usage/codex.rs#L1397-L1435)
- Codex activity 通过 App Server 提取账户活动统计，可作为历史活跃度补充。[Codex activity 源码](https://github.com/junhoyeo/tokscale/blob/965c59e68d8826714036c2abad2298b2efd245fd/crates/tokscale-cli/src/commands/usage/codex_activity.rs#L18-L49)

**局限与风险**

- 没有 daemon、HTTP 配对 API 或 AIHubMix 类通用余额适配器。
- Codex activity 是账户统计，不是当前线程实时状态。
- Cursor 仍使用私有 endpoint，并可把 access token 复制到自有 credentials 文件。[Cursor API 与只读数据库](https://github.com/junhoyeo/tokscale/blob/965c59e68d8826714036c2abad2298b2efd245fd/crates/tokscale-cli/src/cursor.rs#L294-L384)

**复用建议**

用作历史 token 聚合的对照实现和测试 oracle；把“不刷新／不改写提供商凭证”写入 Companion 的强制安全规则。若 OpenUsage 某个 provider 解析失败，可选择调用 Tokscale JSON 作为临时 fallback，但不应同时维护两套长期主流程。

综合评价：**A（解析引擎），C（完整 Companion）。**

### 5.4 TokenTracker：覆盖广，但认证写回与私有 API 风险较高

项目： [xiufengsun/TokenTracker](https://github.com/xiufengsun/TokenTracker/tree/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec)  
许可证： [MIT](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/LICENSE)  
核验版本：提交 f3da4c5；[v0.88.3](https://github.com/xiufengsun/TokenTracker/releases/tag/v0.88.3)

**优势**

- README 明确支持 macOS、Windows、Linux，Node 20+，同时覆盖 Codex、Cursor 与多种 AI 工具。[README：安装与平台](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/README.md#L75-L94)
- Cursor 从本地 SQLite 读取 cursorAuth/accessToken，在内存中构造 WorkosCookie；未发现对 Cursor state.vscdb 的写入。[Cursor 配置源码](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/src/lib/cursor-config.js#L44-L99)
- provider 适配、Dashboard 和多平台路径处理具有参考价值。

**关键风险**

- Codex token refresh 会调用 OAuth 刷新，然后原子替换 Codex auth.json。这会让一个“只读监控器”改变上游登录状态，产生与 Codex CLI／Desktop 并发刷新、令牌轮换或文件损坏的风险。[Codex token refresh 与写回](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/src/lib/codex-token-refresh.js#L38-L128)
- Codex 额度主要直接调用 semi-private wham/usage 及相关 endpoint，稳定性不如官方 App Server。[额度接口源码](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/src/lib/usage-limits.js#L476-L567)
- Cursor 同样依赖私有 export-usage-events-csv 与 usage-summary 接口。[Cursor 私有 API](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/src/lib/cursor-config.js#L136-L185)
- serve 默认只绑定 127.0.0.1:7680，没有为 ESP32 设计的设备配对、最小 DTO 与局域网暴露策略。[Web 服务源码](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/src/commands/serve.js#L14-L25)
- 会话分析是历史／partial JSONL 扫描，活跃文件可能被跳过；没有 working、waitingOnApproval、等待输入等官方状态。[Session analytics](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/src/lib/session-analytics.js#L1097-L1196)
- README 记录了匿名 telemetry 及关闭方式。专用 Companion 应默认关闭，而不是要求用户主动 opt out。[README：隐私与 telemetry](https://github.com/xiufengsun/TokenTracker/blob/f3da4c50569cefd17cf0995d86d88b5bdd6ad2ec/README.md#L292-L322)

**复用建议**

只参考 UI、provider 注册方式、多平台路径和打包；禁止移植 Codex token refresh／auth.json 写回；私有 API 只能作为显式 fallback。

综合评价：**B-（参考），D（原样作为 ESP32 Companion）。**

### 5.5 caut：实现与许可证都不适合作为基线

项目： [coding_agent_usage_tracker](https://github.com/Dicklesworthstone/coding_agent_usage_tracker/tree/d6dc2394dfba511a983a20dcc06b6a81e802ca7e)

README 声称支持多种 coding agents 和双平台，但 provider 主模块实际只导出 Claude 与 Codex，Cursor 相关内容没有形成同等级可用实现。[Provider 模块源码](https://github.com/Dicklesworthstone/coding_agent_usage_tracker/blob/d6dc2394dfba511a983a20dcc06b6a81e802ca7e/src/providers/mod.rs#L1-L9)

其 LICENSE 在 MIT 文本上增加了针对 OpenAI 与 Anthropic 的权利限制，不是无条件 MIT，也增加了衍生与分发风险。[许可证 rider](https://github.com/Dicklesworthstone/coding_agent_usage_tracker/blob/d6dc2394dfba511a983a20dcc06b6a81e802ca7e/LICENSE#L12-L49)

综合评价：**排除。**

## 6. 单一平台或单一 provider 的重要参考

以下项目不满足“Mac + Windows、Codex + Cursor 一体化”的主要求，但有局部可复用价值。

### 6.1 abtop：独立 Codex 会话旁路推断的最佳参考

项目： [graykode/abtop](https://github.com/graykode/abtop/tree/148f64d04663181850519fb36e3c2ca84a9136a6)  
许可证： [MIT](https://github.com/graykode/abtop/blob/148f64d04663181850519fb36e3c2ca84a9136a6/LICENSE)  
核验版本：[v0.5.3](https://github.com/graykode/abtop/releases/tag/v0.5.3)

- 支持 macOS、Linux、Windows，覆盖 Codex、Claude、OpenCode，提供 JSON 输出和 library 接口，但没有 Cursor。[README](https://github.com/graykode/abtop/blob/148f64d04663181850519fb36e3c2ca84a9136a6/README.md#L3-L18)
- Codex 通过进程发现、lsof／平台路径和 rollout JSONL 建立映射。[Codex collector](https://github.com/graykode/abtop/blob/148f64d04663181850519fb36e3c2ca84a9136a6/src/collector/codex.rs#L16-L27)
- 状态算法是明确的推断：进程消失或 task_complete 为 Done；有活跃子进程／未完成 tool 为 Executing；最后一条是未响应用户消息为 Thinking；其余为 Waiting；进程归属不明为 Unknown。[状态推断源码](https://github.com/graykode/abtop/blob/148f64d04663181850519fb36e3c2ca84a9136a6/src/collector/codex.rs#L553-L592)
- Windows 的 PID 到 JSONL 映射主要依靠最近文件时间，置信度弱于能够使用 lsof 的平台。[Windows 映射源码](https://github.com/graykode/abtop/blob/148f64d04663181850519fb36e3c2ca84a9136a6/src/collector/codex.rs#L777-L813)
- JSON 可能包含聊天摘要、路径和命令，README 也明确提示敏感数据风险。[隐私说明](https://github.com/graykode/abtop/blob/148f64d04663181850519fb36e3c2ca84a9136a6/README.md#L192-L196)

建议只移植判定思路和最小统计，不转发其原始 JSON；状态对外必须带 confidence=inferred，且不能伪装成 waitingOnApproval。

### 6.2 CodexMonitor：精确状态可行性的参考，不是旁路监控器

项目： [Dimillian/CodexMonitor](https://github.com/Dimillian/CodexMonitor/tree/dd61b9abd37de5ded86e82b9fe8a83fd49d46fa5)  
许可证： [MIT](https://github.com/Dimillian/CodexMonitor/blob/dd61b9abd37de5ded86e82b9fe8a83fd49d46fa5/LICENSE)

- Tauri 应用支持 macOS、Linux、Windows 构建，但只面向 Codex。
- 它为 workspace 启动并拥有 App Server，处理 thread/status/changed、approval、用户输入、turn 事件，因此能对自己管理的线程提供 VERIFIED 状态。[README：App Server 与审批](https://github.com/Dimillian/CodexMonitor/blob/dd61b9abd37de5ded86e82b9fe8a83fd49d46fa5/README.md#L7-L28) [事件处理源码](https://github.com/Dimillian/CodexMonitor/blob/dd61b9abd37de5ded86e82b9fe8a83fd49d46fa5/src/hooks/useAppServerEvents.ts#L197-L334)
- 它还调用 account/rateLimits/read 获取额度。[Codex core](https://github.com/Dimillian/CodexMonitor/blob/dd61b9abd37de5ded86e82b9fe8a83fd49d46fa5/src-tauri/src/codex_core.rs#L617-L624)

它证明“官方精确状态”可以实现，但前提是 Companion 成为 Codex 前端／编排器；不能据此承诺观察任意已运行的 Codex Desktop、CLI 或 Cursor 内嵌会话。

### 6.3 codex-usage-desktop：Windows App Server 启动兼容参考

项目： [itvincent-git/codex-usage-desktop](https://github.com/itvincent-git/codex-usage-desktop/tree/fbefd8da7f8793321ded2330d8882238ce7721e9)  
许可证： [MIT](https://github.com/itvincent-git/codex-usage-desktop/blob/fbefd8da7f8793321ded2330d8882238ce7721e9/LICENSE)

这是 Codex-only 的 Tauri 项目，支持 macOS、Windows 10／11 和 WSL fallback。它对 Windows 的 .cmd、WSL、超时与进程清理处理有参考价值；但额度仍以私有 wham 为主、App Server 为 fallback，也不提供任意现有会话的真实活动状态。

### 6.4 CodexBar：优秀的 provider 归一化参考，但 App 不支持 Windows

项目： [steipete/CodexBar](https://github.com/steipete/CodexBar/tree/171c2dce44d1e48cb1e9fab57c24df2a773fba2b)  
许可证： [MIT](https://github.com/steipete/CodexBar/blob/171c2dce44d1e48cb1e9fab57c24df2a773fba2b/LICENSE)

CodexBar 同时支持 Codex、Cursor 等多个 provider，解析和测试体系值得参考；但桌面 App 是 macOS-only，CLI 也没有 Windows 基线。其 Codex active／idle 主要依赖进程与 JSONL mtime，不是官方线程状态。

### 6.5 Cursor-only 项目

- [Tendo33/cursor-usage-tracker](https://github.com/Tendo33/cursor-usage-tracker/tree/dae829e6c7d77b7af2fe2af88214274f12d364b3)：MIT、跨平台 VS Code 扩展；从本地读取 token 并调用私有 usage／Stripe／RPC endpoint，可作为兼容测试参考，但没有 LAN Companion。
- [onllm-dev/onWatch](https://github.com/onllm-dev/onWatch/tree/32fc35d7a096b9fc67b761607467617f4774cc45)：GPL-3.0；会读取 access 与 refresh token、调用私有 OAuth 刷新，并把旋转后的凭证写回 Cursor state.vscdb／keychain。与只读边界冲突，不建议原样采用。
- [ofershap/cursor-usage-tracker](https://github.com/ofershap/cursor-usage-tracker/tree/5935d114ddd6d12bbed973d23d2351f2725cdd8d)：使用官方团队／Admin API key，适用于企业账户，不解决个人 Cursor 用量。

## 7. 推荐的 Companion 数据与安全边界

### 7.1 组件分层

| 层 | 职责 | 建议来源 |
|---|---|---|
| Provider collectors | Codex、Cursor、AIHubMix、DeepSeek 采集 | OpenUsage 模块为主 |
| Codex official bridge | App Server 配额与被管理线程事件 | 官方协议；参考 Token Monitor、CodexMonitor |
| Passive session observer | 已存在 Codex 进程和 JSONL 推断 | 参考 abtop |
| Historical aggregation | 日／周／模型 token 汇总与回归校验 | OpenUsage 主实现，Tokscale 对照 |
| Normalizer | 统一 quota、balance、session DTO | 自研最小 schema；参考 Token Monitor |
| Device API | 配对、鉴权、轮询／SSE、脱敏 | 自研；借鉴 OpenUsage Hub 和 Token Monitor |
| ESP32 client | 只读取聚合结果，不接触凭证 | 本项目固件 |

### 7.2 会话状态契约

建议 Companion 返回明确的数据来源与置信度，避免小屏幕把推断结果展示成事实：

| source | confidence | 允许显示的状态 |
|---|---|---|
| codex_app_server_owned | verified | Running、Waiting approval、Waiting input、Completed、Failed |
| process_jsonl_observer | inferred | Running、Recent、Ended、Unknown |
| none | unavailable | Unavailable |

规则：

- waitingOnApproval 只能在当前 Companion 收到官方 approval request 或官方 activeFlags 时显示；
- 仅凭 JSONL 停止增长不能显示“等待审批”，只能显示 Recent／Unknown；
- Windows 的进程与 JSONL 映射若无法唯一确定，应降级 Unknown；
- UI 用图标或短标签区分 VERIFIED 与 INFERRED；
- 会话标题可能含用户 prompt，默认只显示“Codex 会话 1／2”；标题展示必须是可选隐私设置。

### 7.3 凭证规则

1. OAuth access token、refresh token、cookie、API key 只存在电脑侧。
2. ESP32 永不接收、缓存或代理 provider 凭证。
3. Companion 不刷新、不轮换、不回写 Codex auth.json、Cursor state.vscdb 或系统 keychain。
4. access token 失效时返回 AUTH_STALE，并提示用户在原应用重新登录。
5. 自定义余额 provider 的密钥存储使用系统 credential store；配置 JSON 只保存引用 ID。
6. 日志中禁止输出 Authorization、Cookie、完整响应体和本地绝对路径。
7. TokenTracker 的 Codex auth 写回路径和 onWatch 的 Cursor token 回写路径不得移植。

### 7.4 Cursor 实验性适配规则

- provider 标记 experimental=true；
- 私有 endpoint 和响应 schema 有独立版本号；
- 每个 endpoint 设置短超时、错误隔离和 schema validation；
- 升级失败只影响 Cursor 卡片，不能阻断 Codex 与串口功能；
- 首选从 state.vscdb 只读 access token，每次请求前重新读取；
- 禁止读取 refresh token，禁止主动刷新；
- macOS 与 Windows 都必须用真实个人账户做兼容测试，不能只依靠路径单元测试；
- 若未来 Cursor 发布个人官方 API，应优先迁移并保留私有 adapter 作为短期 fallback。

### 7.5 ESP32 专用最小 DTO

设备接口只应包含：

- provider ID 与用户自定义显示名；
- 当前余额、货币、额度已用百分比、周期重置时间；
- Codex 会话数量与脱敏后的状态；
- 数据时间戳、陈旧标记、错误代码；
- Companion 版本与 schema 版本。

不得包含：

- access／refresh token、cookie、API key；
- 邮箱、账户 ID、机器名；
- prompt、聊天内容、tool 参数；
- workspace 绝对路径、文件名、命令；
- OpenUsage 的 Raw、Attributes、Diagnostics 原样对象；
- Cursor 内部完整 Composer 记录。

### 7.6 局域网接口最低要求

- 默认仅绑定 127.0.0.1；用户在 Web 配置页开启“允许设备访问”后才绑定局域网地址；
- 首次配对使用一次性短码，换取可撤销的设备 Bearer token；
- token 使用常量时间比较，支持轮换与逐设备撤销；
- API 请求体设置较小上限，配置读写接口启用 CSRF／Origin 校验；
- 设置读写超时、轮询频率上限与 IP 级速率限制；
- ESP32 只调用专用 /api/v1/device-summary，不调用通用调试或完整 snapshot 接口；
- Web 串口页面与 AI 配置页面共用同一认证会话，但串口 WebSocket 与设备只读 API 使用不同 scope；
- 非 HTTPS 的局域网模式应在 UI 中明确提示；后续可增加本地 CA 或反向代理支持。

## 8. 排名与最终建议

### 8.1 项目排名

1. **OpenUsage**：最佳跨平台综合采集主体。Cursor 只读路径、Go 单文件与 daemon 最符合部署目标；必须新增脱敏设备 API，并把 Codex 改为官方 App Server first。
2. **Token Monitor**：最佳局域网鉴权、第三方余额适配和 schema 参考；Electron 过重，Cursor credential 复制策略不应照搬。
3. **Tokscale**：最佳历史聚合、原生 CLI 与凭证写入边界参考；缺少常驻 Web 层和精确会话状态。
4. **abtop**：最佳独立 Codex 会话旁路推断模块；只适合作为 Codex 辅助组件，输出必须脱敏并标记 inferred。
5. **TokenTracker**：多 provider、UI 和跨平台兼容有价值，但 Codex auth.json 写回、私有 API 优先和无设备安全接口，使其不适合作为底座。
6. **caut／onWatch**：分别因实现完整度与许可证、凭证回写问题排除。

### 8.2 推荐实施路线

**推荐路线 A**

- Go Companion；
- OpenUsage provider 与跨平台层；
- 官方 Codex App Server 配额；
- abtop 风格旁路会话观察；
- Token Monitor 风格的配对、最小 DTO 与自定义余额 adapter；
- Tokscale 作为开发期聚合校验工具；
- ESP32 只消费脱敏 device-summary。

这一方案在可维护性、安全边界、Windows 部署与后续扩展之间最均衡。

**备选路线 B**

- 以 Token Monitor 为现成桌面 Hub；
- Tokscale 负责历史聚合；
- 另外开发精简的 ESP32 API adapter。

它能更快出原型，但 Electron 资源占用、模块耦合和 Cursor token 复制会形成后续技术债。

**不建议路线**

- 直接 fork TokenTracker 后开放 7680 到局域网；
- 让 ESP32 保存 AIHubMix、DeepSeek、Codex 或 Cursor 的真实凭证；
- 把 JSONL mtime 推断显示为“等待审批”；
- 直接把 OpenUsage /v1/snapshots 或 abtop 原始 JSON 发送给设备；
- 在监控程序中刷新并写回 Codex／Cursor 的上游认证文件。

## 9. 已确认的实施决策

本报告的研究结论已经转化为以下开发基线：

1. 独立运行的 Codex 会话只提供 `INFERRED` 状态，不冒充官方精确状态。
2. V1 不接管或启动用户的 Codex Desktop、IDE 或 CLI 会话。
3. Cursor 私有接口失效时保留页面，显示 `STALE/UNAVAILABLE`。
4. 会话标题默认隐藏；设备只显示用户别名或脱敏工程名。
5. Companion 采用 Go 后台程序、菜单栏/托盘入口和内置 Web SPA；通过 LaunchAgent/Task Scheduler 登录自启动。
6. 第三方 Provider 凭据统一保存在 macOS Keychain 或 Windows Credential Manager。
7. 正式采用路线 A：OpenUsage 采集主体、官方 Codex App Server、abtop 风格旁路推测、Token Monitor 安全契约和最小设备 DTO。

具体产品交互、串口状态机、配对、备份和验收要求以 [开发文档](../DEVELOPMENT.md) 为准。
