/* PROTOTYPE ONLY — disposable UI exploration with in-memory mock data. */
"use strict";

const pageGroups = [
  {
    id: "home",
    label: "Home",
    icon: "grid",
    items: [
      { id: "overview", label: "Overview", icon: "grid" },
    ],
  },
  {
    id: "ai",
    label: "AI Collection",
    icon: "spark",
    items: [
      { id: "providers", label: "AI Providers", icon: "spark" },
      { id: "provider-editor", label: "Provider Editor", icon: "settings" },
      { id: "history", label: "Usage History", icon: "clock" },
      { id: "sessions", label: "Codex Sessions", icon: "session" },
    ],
  },
  {
    id: "serial",
    label: "Serial Lab",
    icon: "terminal",
    items: [
      { id: "serial", label: "Serial Terminal", icon: "terminal" },
      { id: "serial-presets", label: "Serial Presets", icon: "session" },
    ],
  },
  {
    id: "devices",
    label: "Devices",
    icon: "device",
    items: [
      { id: "devices", label: "Deck Inventory", icon: "device" },
      { id: "network", label: "Network & Trust", icon: "network" },
      { id: "setup", label: "Setup / Recovery", icon: "device" },
      { id: "deck", label: "Deck RLCD", icon: "chip" },
    ],
  },
  {
    id: "system",
    label: "System",
    icon: "settings",
    items: [
      { id: "system", label: "System Settings", icon: "settings" },
      { id: "updates", label: "Firmware Updates", icon: "upload" },
      { id: "backup", label: "Backup & Restore", icon: "shield" },
      { id: "diagnostics", label: "Diagnostics", icon: "search" },
      { id: "tray", label: "Tray / Menu", icon: "more" },
      { id: "login", label: "Access / Login", icon: "user" },
    ],
  },
];

const allPages = pageGroups.flatMap((group) => group.items);
const hashPage = location.hash.replace(/^#\/?/, "");

const state = {
  page: allPages.some((page) => page.id === hashPage) ? hashPage : "overview",
  mobileNav: false,
  stateInspector: false,
  viewState: "default",
  loggedIn: true,
  modal: null,
  toast: null,
  serialMode: "text",
  webTx: false,
  serialPaused: false,
  setupStep: "wifi",
  setupNetwork: "Studio-2G",
  deckScreen: "codex",
  providerFilter: "all",
  historyRange: "7d",
  sessionFilter: "all",
  trayState: "running",
  providerTest: null,
  otaProgress: 0,
  lanManagement: false,
  providerEditorTab: "request",
  historyProvider: "all",
  selectedDevice: "desk",
  backupStep: "overview",
  diagnosticsTab: "health",
  updateChannel: "stable",
  presetFilter: "all",
  accessStep: "expired",
};

// Visible-copy localization for the selected Chinese UI. Product names, protocol
// names, commands, identifiers and wire-level status codes intentionally remain.
const uiTextReplacements = [
  ["Provider credentials stay on this computer. Deck receives only normalized, display-safe snapshots.", "Provider 凭据保留在此电脑上。Deck 只接收规范化、适合屏幕显示的快照。"],
  ["Management sessions end after eight hours. No device connection was revoked.", "管理会话将在八小时后结束，不会因此撤销任何设备连接。"],
  ["Multiple browsers may observe, but cannot send until one browser acquires the exclusive Web TX lease.", "多个浏览器可以同时观察；只有一个浏览器取得独占 Web TX Lease 后才能发送。"],
  ["They remain disabled while USB owns TX, the terminal is paused, or the Serial Session has ended.", "当 USB 持有 TX、终端暂停或串口会话结束时，预设始终不可发送。"],
  ["It creates a local 30-second single-use grant, exchanges it for an HttpOnly session, then removes the token from the visible URL.", "它会创建一个 30 秒有效的本机一次性授权，兑换为 HttpOnly 会话后立即从可见 URL 中移除 Token。"],
  ["Build a password-protected age archive containing user-entered Provider settings, secrets, Web settings and cached device metadata.", "创建受密码保护的 age 归档，包含用户录入的 Provider 设置、密钥、Web 设置和缓存的设备元数据。"],
  ["Decrypt locally, inspect a redacted preview, choose merge, replace or Providers-only, then resolve each conflict.", "在本机解密并检查脱敏预览，选择合并、替换或仅导入 Provider，然后逐项解决冲突。"],
  ["Pairing tokens, Web sessions, history database and serial buffers are never exported.", "配对 Token、Web 会话、历史数据库和串口缓冲永远不会导出。"],
  ["Running image is valid. Rollback image 0.0.9 remains available in ota_1.", "当前镜像有效，回滚镜像 0.0.9 仍保留在 ota_1。"],
  ["Plain HTTP is accepted only for private RFC1918 addresses and always shows a warning.", "仅私有 RFC1918 地址允许使用 HTTP，并且始终显示警告。"],
  ["Credential value is injected from the OS vault and never shown again.", "凭据值由 OS 凭据库注入，之后不会再次显示。"],
  ["Write-through; success only after Deck acknowledgement.", "直写 Deck；只有收到 Deck 确认后才显示成功。"],
  ["History database is deliberately excluded from encrypted configuration backups.", "历史数据库明确排除在加密配置备份之外。"],
  ["S3-RLCD-A17F · random WPA2 password appears only on Deck.", "S3-RLCD-A17F · 随机 WPA2 密码只显示在 Deck 上。"],
  ["Deck pins the TLS certificate fingerprint before committing.", "Deck 在提交配置前固定 TLS 证书指纹。"],
  ["Token rotation and revocation require explicit confirmation.", "Token 轮换和信任撤销都需要明确确认。"],
  ["File activity alone cannot prove waiting approval or active generation.", "仅凭文件活动无法证明正在等待批准或生成内容。"],
  ["Unavailable never appears as numeric zero.", "不可用数据绝不会以数值 0 显示。"],
  ["No title, prompt, response or path", "不保存标题、Prompt、回复或路径"],
  ["No prompts, paths, secrets or serial body", "不包含 Prompt、路径、密钥或串口正文"],
  ["No trustworthy current evidence", "没有可信的当前证据"],
  ["No unexpected restart", "没有异常重启"],
  ["Show exact local cause, keep Start available", "显示准确的本机原因，并保留启动操作"],
  ["All export fields are redacted before packaging", "所有导出字段都会在打包前脱敏"],
  ["All health checks passed", "所有健康检查均已通过"],
  ["Any failure triggers rollback", "任何失败都会触发回滚"],
  ["BOOT/UART flashing remains the last-resort recovery path.", "BOOT/UART 烧录始终作为最终恢复路径。"],
  ["Changes affect future snapshots.", "更改只影响后续快照。"],
  ["Automatic update checks", "自动检查更新"],
  ["Download never starts without confirmation", "未经确认绝不开始下载"],
  ["Local background service and startup behavior", "本机后台服务与启动行为"],
  ["Only confirmed device-owned values show success", "只有设备已确认的值才显示成功"],
  ["Signed A/B OTA with health confirmation", "带健康确认的签名 A/B OTA"],
  ["User-confirmed, signed and board-specific.", "由用户确认、带签名且匹配指定板卡。"],
  ["Required after first boot.", "首次启动后必须执行。"],
  ["Required before installation", "安装前必须保留"],
  ["Checks only; downloads need confirmation", "仅执行检查；下载需要确认"],
  ["ESP32-S3-RLCD-4.2 must match", "必须匹配 ESP32-S3-RLCD-4.2"],
  ["Local device results and recovery evidence.", "本机设备结果与恢复证据。"],
  ["Failures stay isolated by boundary.", "故障保持在各自边界内。"],
  ["No product telemetry endpoint exists.", "产品不包含遥测端点。"],
  ["No silent updates", "不静默更新"],
  ["No external telemetry", "无外部遥测"],
  ["No telemetry", "无遥测"],
  ["Management and device tokens use separate authority.", "管理 Token 与设备 Token 使用独立权限。"],
  ["Normally, choose Open Console from the tray for automatic one-time access.", "通常可从托盘选择“打开控制台”，自动获得一次性访问权限。"],
  ["Open Console uses a one-time desktop grant", "打开控制台使用桌面端一次性授权"],
  ["Previous session expired", "上一个会话已过期"],
  ["Loopback only by default", "默认仅允许本机回环访问"],
  ["Target TX remains high-impedance", "目标 TX 保持高阻态"],
  ["Normalized hourly totals · no prompt content stored", "规范化小时总量 · 不保存 Prompt 内容"],
  ["Active device · S3-RLCD-A17F", "当前设备 · S3-RLCD-A17F"],
  ["Provider snapshot", "Provider 快照"],
  ["No secret required", "无需密钥"],
  ["Secret stored in OS vault", "密钥存储在 OS 凭据库"],
  ["Experimental read-only", "实验性只读"],
  ["Local app server", "本地 App Server"],
  ["Private HTTP", "私有网络 HTTP"],
  ["HTTP request", "HTTP 请求"],
  ["Structured configuration, never an executable command.", "结构化配置，绝不是可执行命令。"],
  ["Custom HTTPS JSON", "自定义 HTTPS JSON"],
  ["Every 5 minutes", "每 5 分钟"],
  ["Every 15 minutes", "每 15 分钟"],
  ["Every hour", "每小时"],
  ["Included in KEY page cycle", "包含在 KEY 页面循环中"],
  ["Remote endpoints require HTTPS", "远程端点必须使用 HTTPS"],
  ["Optional normalized field", "可选规范化字段"],
  ["Missing fields collapse", "缺失字段自动折叠"],
  ["The Deck must never display zero when the upstream value is unknown.", "上游值未知时，Deck 绝不能显示为 0。"],
  ["Declarative only—no scripts.", "仅支持声明式规则，不允许脚本。"],
  ["Redacted response preview", "脱敏响应预览"],
  ["Deck snapshot", "Deck 快照"],
  ["What the device is allowed to receive", "允许发送给设备的数据"],
  ["Transport boundary", "传输边界"],
  ["Validated before every request.", "每次请求前都会验证。"],
  ["Block cross-host redirects", "阻止跨主机重定向"],
  ["Redirects to another hostname fail closed", "重定向到其他主机名时拒绝请求"],
  ["Limit response body", "限制响应正文"],
  ["Hard limit 256 KiB", "硬性上限 256 KiB"],
  ["Redact diagnostic preview", "诊断预览脱敏"],
  ["Headers and mapped secrets are masked", "Header 与映射密钥均被遮盖"],
  ["10 second request timeout", "请求超时 10 秒"],
  ["Stored by the operating system vault.", "由操作系统凭据库保存。"],
  ["Replace credential", "替换凭据"],
  ["Remove credential", "移除凭据"],
  ["Configure request, mappings, redacted validation and credential boundaries as one inspectable workflow.", "在一个可检查的流程中配置请求、映射、脱敏验证和凭据边界。"],
  ["Run test again", "重新测试"],
  ["Copy redacted JSON", "复制脱敏 JSON"],
  ["Back to Providers", "返回 Provider 列表"],
  ["Save changes", "保存更改"],
  ["Usage History", "用量历史"],
  ["7-day tokens", "7 天 Token"],
  ["Estimated spend", "预估费用"],
  ["Across 3 priced Providers", "覆盖 3 个计费 Provider"],
  ["Hourly normalized rows", "小时级规范化记录"],
  ["Oldest record", "最早记录"],
  ["Export CSV", "导出 CSV"],
  ["Clear history…", "清空历史…"],
  ["All Providers", "全部 Provider"],
  ["Token activity", "Token 活动"],
  ["Hourly total", "小时总量"],
  ["Input", "输入"],
  ["Cached", "缓存"],
  ["Output + reasoning", "输出与推理"],
  ["History controls", "历史设置"],
  ["Save usage history", "保存用量历史"],
  ["Hourly normalized snapshots on this computer", "在此电脑保存小时级规范化快照"],
  ["Automatic retention", "自动保留策略"],
  ["Delete rows older than 90 days", "删除超过 90 天的记录"],
  ["Store anonymous session totals", "保存匿名会话总量"],
  ["local storage", "本机存储"],
  ["Privacy-safe activity", "隐私安全活动"],
  ["Export anonymous CSV", "导出匿名 CSV"],
  ["Search sessions", "搜索会话"],
  ["Session token trend", "会话 Token 趋势"],
  ["Aggregated values only", "仅显示聚合值"],
  ["Confidence semantics", "可信度含义"],
  ["Companion directly owns or observes state", "Companion 直接拥有或观测该状态"],
  ["Estimated from process or JSONL evidence", "根据进程或 JSONL 证据推断"],
  ["“RUNNING?” includes a question mark", "“运行中？”包含问号"],
  ["Bounded serial session", "有边界的串口会话"],
  ["Serial input is owned by USB", "串口输入当前由 USB 持有"],
  ["Acquire Web TX", "取得 Web TX"],
  ["Release Web TX", "释放 Web TX"],
  ["Session export prepared", "会话导出已准备"],
  ["Line ending", "行结束符"],
  ["Use current terminal setting", "使用当前终端设置"],
  ["Reusable commands", "可复用命令"],
  ["Presets do not bypass TX ownership", "预设不能绕过 TX 所有权"],
  ["All devices", "所有设备"],
  ["New preset", "新建预设"],
  ["Preset editor", "预设编辑器"],
  ["Unsaved example", "未保存示例"],
  ["Save preset", "保存预设"],
  ["Physical fleet", "实体设备"],
  ["Deck Inventory", "Deck 清单"],
  ["Trusted Decks", "受信任 Deck"],
  ["paired ·", "已配对 ·"],
  ["online", "在线"],
  ["Maximum 5 profiles per Deck", "每块 Deck 最多保存 5 个 Profile"],
  ["Last heartbeat", "最后心跳"],
  ["Temperature", "温度"],
  ["Serial", "串口"],
  ["Active Companion", "当前 Companion"],
  ["AI Page", "AI 页面"],
  ["Preview RLCD", "预览 RLCD"],
  ["Manage trust", "管理信任"],
  ["Device-owned settings", "设备自有设置"],
  ["UART preset", "UART 预设"],
  ["Screen refresh", "屏幕刷新"],
  ["Timezone cache", "时区缓存"],
  ["Save and wait for Deck", "保存并等待 Deck 确认"],
  ["Trust & connectivity", "信任与连接"],
  ["Pair a Deck", "配对 Deck"],
  ["Companion listeners", "Companion 监听器"],
  ["Allow LAN management", "允许 LAN 管理访问"],
  ["Enable LAN access", "启用 LAN 访问"],
  ["Disable LAN access", "停用 LAN 访问"],
  ["Pairing workflow", "配对流程"],
  ["Connect computer to Deck Setup AP", "将电脑连接到 Deck Setup AP"],
  ["Generate a six-digit code here", "在此生成六位配对码"],
  ["Enter Hub address and code on Deck Setup", "在 Deck Setup 中输入 Hub 地址和配对码"],
  ["No active pairing code", "当前没有有效配对码"],
  ["Generate code", "生成配对码"],
  ["Rotate token", "轮换 Token"],
  ["Revoke", "撤销"],
  ["Maintenance", "维护"],
  ["Companion uptime", "Companion 运行时长"],
  ["Deck temperature", "Deck 温度"],
  ["Log storage", "日志存储"],
  ["Update channel", "更新通道"],
  ["Deck firmware", "Deck 固件"],
  ["Running slot", "当前分区"],
  ["Rollback slot", "回滚分区"],
  ["Device calibration", "设备校准"],
  ["Local time source", "本地时间源"],
  ["Save and wait for Deck", "保存并等待 Deck"],
  ["Diagnostics & privacy", "诊断与隐私"],
  ["Rotating logs", "轮转日志"],
  ["Crash reports", "崩溃报告"],
  ["Never sent outside this computer", "绝不发送到此电脑之外"],
  ["Include Deck memory ring", "包含 Deck 内存环"],
  ["Build diagnostics.zip", "生成 diagnostics.zip"],
  ["Clear local logs", "清空本机日志"],
  ["Encrypted backup", "加密备份"],
  ["Provider configuration and user-entered secrets only", "仅包含 Provider 配置与用户录入的密钥"],
  ["Device trust is excluded", "不包含设备信任"],
  ["Export .age backup", "导出 .age 备份"],
  ["Import & preview", "导入并预览"],
  ["Companion lifecycle", "Companion 生命周期"],
  ["Start at login", "登录时启动"],
  ["Restart Companion", "重启 Companion"],
  ["Stop Companion", "停止 Companion"],
  ["Signed delivery", "签名交付"],
  ["Firmware Updates", "固件更新"],
  ["Select signed firmware", "选择已签名固件"],
  ["Check stable channel", "检查稳定通道"],
  ["Update policy", "更新策略"],
  ["Automatic checks", "自动检查"],
  ["Preserve rollback slot", "保留回滚分区"],
  ["Confirm board model", "确认板卡型号"],
  ["Health confirmation", "健康确认"],
  ["Display initialization", "显示屏初始化"],
  ["Companion handshake", "Companion 握手"],
  ["Update history", "更新历史"],
  ["Portable configuration", "可迁移配置"],
  ["Backup & Restore", "备份与恢复"],
  ["age encrypted", "age 加密"],
  ["Create backup", "创建备份"],
  ["Export configuration", "导出配置"],
  ["Import configuration", "导入配置"],
  ["Choose backup", "选择备份"],
  ["Import preview", "导入预览"],
  ["Resolve conflicts", "解决冲突"],
  ["Result", "结果"],
  ["Evidence without secrets", "不含密钥的诊断证据"],
  ["Diagnostics", "诊断"],
  ["Run health check", "运行健康检查"],
  ["Export bundle", "导出诊断包"],
  ["Health", "健康"],
  ["Event log", "事件日志"],
  ["Privacy report", "隐私报告"],
  ["Support bundle", "支持包"],
  ["Overall health", "整体健康"],
  ["isolated Provider warning", "个隔离的 Provider 警告"],
  ["Event loop", "事件循环"],
  ["Device RTT", "设备 RTT"],
  ["Memory", "内存"],
  ["No growth anomaly", "未发现异常增长"],
  ["Subsystem health", "子系统健康"],
  ["Serial bridge", "串口桥接"],
  ["Deck health", "Deck 健康"],
  ["Current module", "当前模块"],
  ["Tasks", "任务"],
  ["System nominal", "系统正常"],
  ["Last health check", "上次健康检查"],
  ["Review state", "评审状态"],
  ["Default", "默认"],
  ["Loading", "加载中"],
  ["Empty", "空状态"],
  ["Error", "错误"],
  ["Companion ready", "Companion 已就绪"],
  ["Deck online", "Deck 在线"],
  ["Home", "首页"],
  ["AI Collection", "AI 采集"],
  ["AI Providers", "AI Provider"],
  ["Provider Editor", "Provider 编辑器"],
  ["Codex Sessions", "Codex 会话"],
  ["Serial Lab", "串口工作台"],
  ["Serial Terminal", "串口终端"],
  ["Serial Presets", "串口预设"],
  ["Devices", "设备"],
  ["Network & Trust", "网络与信任"],
  ["System Settings", "系统设置"],
  ["Tray / Menu States", "托盘 / 菜单状态"],
  ["Tray / Menu", "托盘 / 菜单"],
  ["Access / Login", "访问 / 登录"],
  ["Overview", "概览"],
  ["Command center", "运行总览"],
  ["Connected Decks", "已连接 Deck"],
  ["Healthy Providers", "健康 Provider"],
  ["Today Tokens", "今日 Token"],
  ["Serial Session", "串口会话"],
  ["Token activity", "Token 活动"],
  ["Provider health", "Provider 健康度"],
  ["View all", "查看全部"],
  ["Manage Provider", "管理 Provider"],
  ["Refresh snapshot", "刷新快照"],
  ["Open sessions", "打开会话"],
  ["Preview screen", "预览屏幕"],
  ["Collection", "采集"],
  ["Add Provider", "添加 Provider"],
  ["Test request", "测试请求"],
  ["Edit", "编辑"],
  ["Deck order", "Deck 顺序"],
  ["Display order", "显示顺序"],
  ["Show on Deck", "在 Deck 显示"],
  ["Request", "请求"],
  ["Data mapping", "数据映射"],
  ["Test preview", "测试预览"],
  ["Security", "安全"],
  ["Display name", "显示名称"],
  ["Template", "模板"],
  ["Method", "方法"],
  ["Polling interval", "轮询间隔"],
  ["Endpoint", "端点"],
  ["Headers", "Header"],
  ["JSON body", "JSON 正文"],
  ["Name", "名称"],
  ["Mode", "模式"],
  ["Payload preview", "Payload 预览"],
  ["Scope", "范围"],
  ["Actions", "操作"],
  ["Text", "文本"],
  ["Search", "搜索"],
  ["Clear", "清除"],
  ["Send", "发送"],
  ["Live", "实时"],
  ["Paused", "已暂停"],
  ["Web clients", "Web 客户端"],
  ["Rate", "速率"],
  ["UART errors", "UART 错误"],
  ["Companion overwrite", "Companion 覆盖"],
  ["ESP overwrite", "ESP 覆盖"],
  ["Physical screen simulator", "实体屏幕模拟器"],
  ["Screen inventory", "屏幕清单"],
  ["First setup", "首次设置"],
  ["Pair Companion", "配对 Companion"],
  ["Codex AI Page", "Codex AI 页面"],
  ["Provider Page", "Provider 页面"],
  ["No Provider hint", "无 Provider 提示"],
  ["STALE snapshot", "过期快照"],
  ["Companion failover", "Companion 故障切换"],
  ["Agent offline", "Companion 离线"],
  ["Serial session", "串口会话"],
  ["Serial stats subview", "串口统计子视图"],
  ["Degraded operation", "降级运行"],
  ["OTA progress", "OTA 进度"],
  ["Rollback result", "回滚结果"],
  ["KEY cycles AI Pages", "KEY 循环 AI 页面"],
  ["BOOT exits Serial", "BOOT 退出串口"],
  ["KEY · Next page", "KEY · 下一页"],
  ["Desktop shell", "桌面端外壳"],
  ["State copy", "状态文案"],
  ["Primary action", "主要操作"],
  ["Runtime action", "运行时操作"],
  ["Error recovery", "错误恢复"],
  ["Open Console", "打开控制台"],
  ["About S3 RLCD Deck", "关于 S3 RLCD Deck"],
  ["Quit", "退出"],
  ["Tray grant", "托盘授权"],
  ["Manual token", "手动 Token"],
  ["Expired", "已过期"],
  ["Rate limited", "已限流"],
  ["Your local AI usage instrument.", "你的本地 AI 用量仪表。"],
  ["Local management", "本机管理"],
  ["Unlock Companion", "解锁 Companion"],
  ["Open from tray grant", "使用托盘授权打开"],
  ["Use token", "使用 Token"],
  ["PROTOTYPE ONLY", "仅供原型评审"],
  ["INSTRUMENT PANEL", "仪表面板"],
  ["ALL PROVIDERS", "全部 Provider"],
  ["ALL", "全部"],
  ["AVAILABLE", "可用"],
  ["ENABLED", "已启用"],
  ["UP TO DATE", "已是最新"],
  ["NOMINAL", "正常"],
  ["HEALTHY", "健康"],
  ["READY", "就绪"],
  ["RUNNING", "运行中"],
  ["STARTING", "正在启动"],
  ["STOPPED", "已停止"],
  ["OFFLINE", "离线"],
  ["ONLINE", "在线"],
  ["UNAVAILABLE", "不可用"],
  ["VERIFIED", "已验证"],
  ["INFERRED", "推断"],
  ["STALE", "已过期"],
  ["RECENT", "最近活动"],
  ["ENDED", "已结束"],
  ["MIXED", "混合"],
  ["DRAFT", "草稿"],
  ["VALID", "有效"],
  ["PASS", "通过"],
  ["DISARMED", "未启用"],
  ["FAILURE", "失败"],
  ["SUCCESS", "成功"],
  ["WARNING", "警告"],
  ["INFO", "信息"],
  ["+8.2% from 7-day average", "较 7 日均值增加 8.2%"],
  ["+8.2% vs previous period", "较上一周期增加 8.2%"],
  ["S3 RLCD Deck Companion · v0.1.0 · Running · 1 Deck connected", "S3 RLCD Deck Companion · v0.1.0 · 运行中 · 已连接 1 块 Deck"],
  ["SHTC3 health check failed", "SHTC3 健康检查失败"],
  ["The code expires in five minutes and can be redeemed once.", "配对码五分钟后过期，且只能使用一次。"],
  ["Stored only after connection validation succeeds.", "仅在连接验证成功后保存。"],
  ["Allowed range −15.0°C to +15.0°C", "允许范围 −15.0°C 至 +15.0°C"],
  ["Explicit host:port + six-digit code", "明确的 host:port 与六位配对码"],
  ["Fresh token + second confirmation", "新鲜 Token 与二次确认"],
  ["Preserved until candidate validates", "保留至候选配置验证成功"],
  ["10 minutes of inactivity", "无操作 10 分钟"],
  ["Available · BOOT hold", "可用 · 长按 BOOT"],
  ["Not installed · AI 页面", "未安装 · AI 页面"],
  ["Not installed · AI Page", "未安装 · AI 页面"],
  ["Pinned WSS · stable", "已固定 WSS · 稳定"],
  ["p95 over last hour", "过去一小时 p95"],
  ["ota_0 · 有效", "ota_0 · 有效"],
  ["OK · CRC valid", "正常 · CRC 有效"],
  ["OK · active config v3", "正常 · 当前配置 v3"],
  ["OK · calibrated", "正常 · 已校准"],
  ["OK · 400×300", "正常 · 400×300"],
  ["RTC + Companion sync", "RTC + Companion 同步"],
  ["RTC only", "仅 RTC"],
  ["Manual file only", "仅手动文件"],
  ["Update channel: stable", "更新通道：稳定"],
  ["May 16", "5 月 16 日"],
  ["Aug 11", "8 月 11 日"],
  ["Aug 12", "8 月 12 日"],
  ["Aug 13", "8 月 13 日"],
  ["Aug 14", "8 月 14 日"],
  ["Aug 15", "8 月 15 日"],
  ["Peak: 149k on Saturday", "峰值：周六 149k"],
  ["Resets in 02:44", "02:44 后重置"],
  ["Cursor marked 已过期", "Cursor 已标记为过期"],
  ["Updated Retry in 44s", "44 秒后重试"],
  ["Updated 47m ago", "47 分钟前更新"],
  ["Updated 26s ago", "26 秒前更新"],
  ["Updated 18s ago", "18 秒前更新"],
  ["Desk Deck · heartbeat 4s ago", "Desk Deck · 4 秒前心跳"],
  ["S3-RLCD-A17F · heartbeat 4s ago", "S3-RLCD-A17F · 4 秒前心跳"],
  ["Last snapshot 10:36:18", "上次快照 10:36:18"],
  ["Last snapshot", "上次快照"],
  ["v0.1.0 · Running", "v0.1.0 · 运行中"],
  ["1 Deck connected", "已连接 1 块 Deck"],
  ["1 Deck 在线 · Loopback only", "1 块 Deck 在线 · 仅本机回环"],
  ["1 Deck 在线", "1 块 Deck 在线"],
  ["2 read · 1 owner", "2 个只读 · 1 个所有者"],
  ["7 days or 50 MiB", "7 天或 50 MiB"],
  ["7 days · 50 MiB cap", "7 天 · 上限 50 MiB"],
  ["62% left", "剩余 62%"],
  ["24.8°C · calibrated", "24.8°C · 已校准"],
  ["4s ago", "4 秒前"],
  ["8 days ago", "8 天前"],
  ["18s ago", "18 秒前"],
  ["47m ago", "47 分钟前"],
  ["Ended 8m ago", "8 分钟前结束"],
  ["Ended 1h ago", "1 小时前结束"],
  ["Installed", "已安装"],
  ["Rolled back", "已回滚"],
  ["Initial paired build", "初始配对版本"],
  ["Companion-owned", "Companion 直接拥有"],
  ["Process observer", "进程观察器"],
  ["JSONL observer", "JSONL 观察器"],
  ["Active Companion: this Mac", "当前 Companion：此 Mac"],
  ["Companion-owned", "Companion 直接拥有"],
  ["Date", "日期"],
  ["Tokens", "Token"],
  ["Providers", "Provider"],
  ["Confidence", "可信度"],
  ["Device ID", "设备 ID"],
  ["Status", "状态"],
  ["Last heartbeat", "最后心跳"],
  ["Firmware", "固件"],
  ["Session", "会话"],
  ["State", "状态"],
  ["Duration", "时长"],
  ["Context", "上下文"],
  ["Source", "来源"],
  ["Board", "板卡"],
  ["Certificate", "证书"],
  ["Channel", "通道"],
  ["Check for update", "检查更新"],
  ["Select firmware", "选择固件"],
  ["Details", "详情"],
  ["Display", "显示屏"],
  ["Existing Wi-Fi", "现有 Wi-Fi"],
  ["Export diagnostics", "导出诊断"],
  ["Export", "导出"],
  ["Failure", "失败"],
  ["Flow states", "流程状态"],
  ["History storage", "历史存储"],
  ["Network", "网络"],
  ["None", "无"],
  ["Offset", "偏移"],
  ["Page 5 of 5", "第 5 页，共 5 页"],
  ["Pairing", "配对"],
  ["Password for Studio-2G", "Studio-2G 密码"],
  ["Payload", "载荷"],
  ["Polling & isolation", "轮询与隔离"],
  ["Preview", "预览"],
  ["Primary", "主要额度"],
  ["Profiles", "配置档案"],
  ["Protocol", "协议"],
  ["Provider API keys never visible", "绝不显示 Provider API Key"],
  ["Recovery invariants", "恢复流程约束"],
  ["Recovery", "恢复"],
  ["Reset target", "重置目标设备"],
  ["Restart flow", "重新开始流程"],
  ["Runtime", "运行时"],
  ["Scan networks", "扫描网络"],
  ["Secrets", "密钥"],
  ["Settings", "设置"],
  ["Stable", "稳定"],
  ["Success", "成功"],
  ["System", "系统"],
  ["Tooltip", "工具提示"],
  ["Validate and activate", "验证并启用"],
  ["Validating", "验证中"],
  ["Version query", "查询版本"],
  ["Weekly", "每周额度"],
  ["Wi-Fi recovery", "Wi-Fi 恢复"],
  ["Tuesday", "周二"],
  ["Tue", "周二"],
  ["MONOCHROME", "单色"],
  ["DECK-OWNED RECOVERY", "DECK 自有恢复"],
  ["DESK DECK · OTA_0", "桌面 Deck · OTA_0"],
  ["PROVIDER WORKSPACE", "PROVIDER 工作区"],
  ["LOCAL HISTORY", "本机历史"],
  ["LINE ENDING", "行结束符"],
  ["PRESETS", "预设"],
  ["SNAPSHOTS", "快照"],
  ["STEP 1 OF 3", "第 1 步，共 3 步"],
  ["HOLD · SERIAL", "长按 · 串口"],
  ["KEY · NEXT", "KEY · 下一页"],
  ["LIVE", "实时"],
  ["TEXT", "文本"],
  ["TODAY TOKENS", "今日 Token"],
  ["HEALTHY PROVIDERS", "健康 Provider"],
  ["CONNECTED DECKS", "已连接 Deck"],
  ["ERROR", "错误"],
  ["−74 dBm · Open", "−74 dBm · 开放网络"],
  ["second", "秒"],
  ["seconds", "秒"],
  ["surface", "个界面"],
  ["surfaces", "个界面"],
  ["Deck RLCD Screens", "Deck RLCD 屏幕"],
  ["Deck-owned recovery", "Deck 自有恢复"],
  ["Provider workspace", "Provider 工作区"],
  ["Local history", "本机历史"],
  ["Step 1 of 3", "第 1 步，共 3 步"],
  ["Snapshots", "快照"],
  ["Retention", "保留期限"],
  ["monochrome", "单色"],
  ["No silent updates", "不静默更新"],
  ["NO SILENT UPDATES", "不静默更新"],
  ["NO EXTERNAL TELEMETRY", "无外部遥测"],
  ["NO TELEMETRY", "无遥测"],
  ["SYSTEM NOMINAL", "系统正常"],
  ["Loopback only", "仅本机回环"],
  ["AP active", "AP 已开启"],
  ["Setup timeout", "Setup 超时时间"],
  ["Temperature offset", "温度偏移"],
  ["USB TX owner", "USB TX 所有者"],
  ["Cursor marked STALE", "Cursor 已标记为过期"],
  ["HEX bytes", "HEX 字节"],
  ["Enter bootloader", "进入 Bootloader"],
  ["Instrument", "仪表"],
  ["Management Web", "管理 Web"],
  ["SQLite history", "SQLite 历史库"],
  ["Health check", "健康检查"],
  ["Optional for POST requests", "POST 请求可选"],
  ["Send to target…", "发送到目标设备…"],
  ["Acquire Web TX to send", "取得 Web TX 后才能发送"],
  ["Search redacted logs", "搜索脱敏日志"],
  ["Search redacted name", "搜索脱敏名称"],
  ["1 second", "1 秒"],
  ["2 seconds", "2 秒"],
  ["5 seconds", "5 秒"],
  ["7 days", "7 天"],
  ["90 days", "90 天"],
  ["+2 sessions", "另有 2 个会话"],
  ["146k tokens", "146k Token"],
  ["template", "模板"],
  ["Map only fields that are present; unknown values stay null.", "只映射实际存在的字段；未知值保持为 null。"],
  ["No shell, JavaScript, arbitrary plugin or executable cURL is accepted.", "不接受 Shell、JavaScript、任意插件或可执行 cURL。"],
  ["Included only in encrypted backup", "仅包含在加密备份中"],
  ["No additional Provider pages are enabled", "尚未启用其他 Provider 页面"],
  ["Values hidden after 24h", "24 小时后隐藏数值"],
  ["Last verified snapshot · 27h ago", "上次验证快照 · 27 小时前"],
  ["Display + recovery remain available", "显示屏与恢复功能仍然可用"],
  ["Restored 0.1.0 from ota_0", "已从 ota_0 恢复 0.1.0"],
  ["Do not disconnect power", "请勿断开电源"],
  ["Configure order in Companion", "请在 Companion 中配置顺序"],
  ["Connect phone or computer to", "用手机或电脑连接"],
  ["Enter Hub address + 6-digit code", "输入 Hub 地址与六位配对码"],
  ["Connecting to Studio-2G", "正在连接 Studio-2G"],
  ["12 seconds elapsed · timeout in 8s", "已用 12 秒 · 8 秒后超时"],
  ["Deck preview: raw 28.8°C → calibrated 24.8°C", "Deck 预览：原始 28.8°C → 校准后 24.8°C"],
  ["Last connected 8 days ago", "上次连接于 8 天前"],
  ["Wi-Fi connected · Studio-2G", "Wi-Fi 已连接 · Studio-2G"],
  ["Open Setup / 恢复", "打开 Setup / 恢复"],
  ["Reconnect 当前 Companion", "重新连接当前 Companion"],
  ["No profile preemption", "配置档案不抢占"],
  ["Last snapshot preserved", "保留上次快照"],
  ["Healthy services continue", "健康服务继续运行"],
  ["Recovery available", "可进入恢复流程"],
  ["Clear saved Wi-Fi…", "清除已保存的 Wi-Fi…"],
  ["Skip for now", "暂时跳过"],
  ["Continue", "继续"],
  ["Cancel candidate", "取消候选配置"],
  ["Make active", "设为当前"],
  ["Manage profiles", "管理配置档案"],
  ["View fingerprint", "查看指纹"],
  ["Save offset", "保存偏移"],
  ["Add mapping", "添加映射"],
  ["Normalized fields", "规范化字段"],
  ["Number divisor", "数值除数"],
  ["Timestamp format", "时间戳格式"],
  ["Transform rules", "转换规则"],
  ["Reset time", "重置时间"],
  ["Used percent", "已用百分比"],
  ["Balance", "余额"],
  ["Currency", "币种"],
  ["Usage", "用量"],
  ["Updated", "更新时间"],
  ["Created", "创建时间"],
  ["Last used", "上次使用"],
  ["Reference", "引用"],
  ["Credential", "凭据"],
  ["Secret", "密钥"],
  ["ACTIVE", "当前"],
  ["ARMED", "已启用"],
  ["CONNECTED", "已连接"],
  ["EXPERIMENTAL", "实验性"],
  ["NEVER SENT", "永不发送"],
  ["RETRY", "重试"],
  ["OK", "正常"],
  ["2 ISSUES", "2 个问题"],
  ["LAST VERIFIED SNAPSHOT", "上次验证快照"],
  ["HEALTHY SERVICES CONTINUE", "健康服务继续运行"],
  ["UART ERRORS", "UART 错误"],
  ["UART ERROR", "UART 错误"],
  ["FIRST SETUP", "首次设置"],
  ["PAIR COMPANION", "配对 Companion"],
  ["COMPANION FAILOVER", "Companion 故障切换"],
  ["COMPANION PROFILES", "Companion 配置档案"],
  ["DEGRADED OPERATION", "降级运行"],
  ["FIRMWARE UPDATE", "固件更新"],
  ["HEALTH CHECK FAILED", "健康检查失败"],
  ["FLOW HEALTH", "流量健康"],
  ["SERIAL STATS", "串口统计"],
  ["SERIAL", "串口"],
  ["SETUP COMPLETE", "Setup 完成"],
  ["UPDATE ROLLED BACK", "更新已回滚"],
  ["QUOTA HIDDEN", "额度已隐藏"],
  ["NO AGENT", "未连接 Companion"],
  ["AGENT OFFLINE", "Companion 离线"],
  ["AGENT OVERWRITE", "Companion 覆盖"],
  ["AGENT ●", "Companion ●"],
  ["AGENT …", "Companion …"],
  ["AP CLOSES", "AP 关闭倒计时"],
  ["BOOT HOLD · SETUP", "长按 BOOT · SETUP"],
  ["BOOT · EXIT", "BOOT · 退出"],
  ["BOOT · RECOVERY", "BOOT · 恢复"],
  ["BOOT · RESTART", "BOOT · 重启"],
  ["KEY · STATS", "KEY · 统计"],
  ["KEY · TOTALS", "KEY · 总量"],
  ["KEY · CODEX", "KEY · CODEX"],
  ["OLD FIRMWARE PRESERVED", "旧固件已保留"],
  ["VIEW DETAILS ON COMPUTER", "在电脑查看详情"],
  ["WEB ON COMPUTER", "在电脑打开 Web"],
  ["WRITING OTA_1", "正在写入 OTA_1"],
  ["REFRESH", "刷新"],
  ["RX RATE", "RX 速率"],
  ["RX TOTAL", "RX 总量"],
  ["TX RATE", "TX 速率"],
  ["TX TOTAL", "TX 总量"],
  ["USB REJECT", "USB 拒绝"],
  ["WEB CLIENTS", "Web 客户端"],
  ["OVERWRITE", "覆盖"],
  ["SETUP AP", "SETUP AP"],
  ["A one-time desktop grant from the tray is being exchanged for an HttpOnly session. The grant is removed from the visible URL immediately.", "正在将托盘的一次性桌面授权兑换为 HttpOnly 会话，授权会立即从可见 URL 中移除。"],
  ["Several invalid management tokens were rejected from this browser. Device connections and Provider collection continue normally.", "此浏览器提交的多个无效管理 Token 已被拒绝，设备连接和 Provider 采集仍会正常运行。"],
  ["Use this only when tray access is unavailable. The token remains in the operating system credential vault.", "仅在托盘访问不可用时使用此方式。Token 保留在操作系统凭据库中。"],
  ["Paste the local management token, never a device or Provider token.", "请粘贴本机管理 Token，绝不要使用设备或 Provider Token。"],
  ["Close this tab or wait for the local cooldown. Token are never logged.", "关闭此标签页或等待本机冷却结束。Token 永远不会写入日志。"],
  ["The last valid data remains unchanged. Companion, Device Hub and Serial ownership were not restarted.", "最后一次有效数据保持不变，Companion、Device Hub 和串口所有权均未重启。"],
  ["The app does not upload this archive automatically.", "应用不会自动上传此归档。"],
  ["Nothing changes until preview, conflict resolution and confirmation finish.", "在完成预览、冲突处理和确认之前，不会修改任何内容。"],
  ["No partial import is ever committed.", "绝不会提交部分导入结果。"],
  ["Authentication fails before any item is read.", "在读取任何项目之前即停止认证。"],
  ["Keep the current configuration and suggest a compatible Companion.", "保留当前配置，并提示使用兼容的 Companion。"],
  ["Hash or manifest mismatch cancels the entire transaction.", "Hash 或清单不匹配时取消整个事务。"],
  ["Use a password you can transfer separately; it cannot be recovered.", "请使用可单独传递的密码；该密码无法找回。"],
  ["Codex/Cursor-owned tokens remain excluded.", "Codex/Cursor 自有 Token 仍然排除在外。"],
  ["age-encrypted, portable between macOS and Windows.", "使用 age 加密，可在 macOS 与 Windows 之间迁移。"],
  ["2 Provider configurations were added, 3 conflicts were resolved, and the previous configuration was preserved as an automatic rollback snapshot.", "已添加 2 项 Provider 配置、解决 3 项冲突，并将旧配置保留为自动回滚快照。"],
  ["12 items imported safely", "已安全导入 12 项"],
  ["Review 3 conflicts", "检查 3 项冲突"],
  ["Backup puts DeepSeek before Cursor", "备份将 DeepSeek 排在 Cursor 之前"],
  ["Backup enables LAN access", "备份将启用 LAN 访问"],
  ["Endpoint and credential changed", "端点和凭据已更改"],
  ["Default action is merge; every replacement remains explicit.", "默认执行合并；每一项替换仍需明确选择。"],
  ["Create encrypted backup", "创建加密备份"],
  ["Create .age backup", "创建 .age 备份"],
  ["Backup password", "备份密码"],
  ["Confirm password", "确认密码"],
  ["Include user-entered Provider credentials", "包含用户录入的 Provider 凭据"],
  ["Export manifest", "导出清单"],
  ["Preview before encryption.", "加密前预览。"],
  ["Provider configs", "Provider 配置"],
  ["User secrets", "用户密钥"],
  ["Web settings", "Web 设置"],
  ["Device metadata", "设备元数据"],
  ["History database", "历史数据库"],
  ["Pairing tokens", "配对 Token"],
  ["Web sessions", "Web 会话"],
  ["Included", "包含"],
  ["Excluded", "排除"],
  ["references", "个引用"],
  ["cached profiles", "个缓存配置档案"],
  ["Import backup", "导入备份"],
  ["Choose another file", "选择其他文件"],
  ["Decrypt and inspect", "解密并检查"],
  ["Safe failure states", "安全失败状态"],
  ["Wrong password", "密码错误"],
  ["Unsupported schema", "不支持的 Schema"],
  ["Damaged archive", "归档已损坏"],
  ["Keep current", "保留当前值"],
  ["Use backup", "使用备份值"],
  ["New items", "新增项目"],
  ["Unchanged", "未更改"],
  ["Conflicts", "冲突"],
  ["Skipped", "已跳过"],
  ["Back", "返回"],
  ["Import Provider only", "仅导入 Provider"],
  ["Confirm merge", "确认合并"],
  ["Replace everything…", "全部替换…"],
  ["Transactional import complete", "事务导入完成"],
  ["Review Provider", "检查 Provider"],
  ["Done", "完成"],
  ["Private endpoint unavailable", "私有端点不可用"],
  ["Snapshot normalized", "快照已规范化"],
  ["Web observer connected", "Web 观察端已连接"],
  ["Snapshot marked stale", "快照已标记为过期"],
  ["Deck heartbeat accepted", "Deck 心跳已接受"],
  ["All levels", "全部级别"],
  ["Warnings & errors", "警告与错误"],
  ["All modules", "全部模块"],
  ["Level", "级别"],
  ["Module", "模块"],
  ["Time", "时间"],
  ["Event", "事件"],
  ["Safe detail", "安全详情"],
  ["Collection boundary", "数据收集边界"],
  ["Runtime data that may be retained locally.", "可能保留在本机的运行时数据。"],
  ["Normalized Provider metrics", "规范化 Provider 指标"],
  ["Anonymous session totals", "匿名会话总量"],
  ["Diagnostic events", "诊断事件"],
  ["Prompt / response content", "Prompt / 回复内容"],
  ["Absolute paths", "绝对路径"],
  ["Serial body", "串口正文"],
  ["Stored · optional", "已保存 · 可选"],
  ["Stored · 90 days", "已保存 · 90 天"],
  ["Stored · 7 days", "已保存 · 7 天"],
  ["Never collected", "从不采集"],
  ["Redacted", "已脱敏"],
  ["Current session buffer only", "仅当前会话缓冲"],
  ["Outbound connections", "对外连接"],
  ["Provider APIs", "Provider API"],
  ["Only configured endpoints", "仅已配置端点"],
  ["Update checks", "更新检查"],
  ["Configured release source", "已配置的发布源"],
  ["Pinned local WSS", "已固定的本地 WSS"],
  ["Analytics", "分析服务"],
  ["Crash upload", "崩溃上传"],
  ["Export privacy report", "导出隐私报告"],
  ["Build support bundle", "生成支持包"],
  ["Preview every category before export.", "导出前预览每个类别。"],
  ["Companion manifest", "Companion 清单"],
  ["Version, platform and module status", "版本、平台和模块状态"],
  ["Redacted event log", "脱敏事件日志"],
  ["Last 24 hours; secrets and paths removed", "最近 24 小时；已移除密钥与路径"],
  ["Deck memory ring", "Deck 内存环"],
  ["Structured health only; no serial body", "仅结构化健康数据；不含串口正文"],
  ["Configuration schema", "配置 Schema"],
  ["Keys only; no values or secret refs", "仅包含字段名；不含值或密钥引用"],
  ["Bundle manifest", "支持包清单"],
  ["Estimated size", "预估大小"],
  ["Review before sharing", "分享前检查"],
  ["Opening local listeners…", "正在打开本机监听器…"],
  ["Listeners are offline", "监听器已离线"],
  ["Port 7777 is already in use", "端口 7777 已被占用"],
  ["Start Companion", "启动 Companion"],
  ["Opening local console", "正在打开本机控制台"],
  ["Continue now", "立即继续"],
  ["Manual recovery", "手动恢复"],
  ["Use management token", "使用管理 Token"],
  ["Management token", "管理 Token"],
  ["Unlock local console", "解锁本机控制台"],
  ["Try again in 00:42", "请在 00:42 后重试"],
  ["Return to token entry", "返回 Token 输入"],
  ["Loading state", "加载状态"],
  ["Empty state", "空状态"],
  ["Recoverable error", "可恢复错误"],
  ["Partial failure", "部分失败"],
  ["Could not load this surface", "无法加载此界面"],
  ["No overview yet", "暂无概览数据"],
  ["Return to populated example", "返回有数据示例"],
  ["Try again", "重试"],
  ["Open Diagnostics", "打开诊断"],
  ["LOCAL ONLY", "仅限本机"],
  ["NO CHANGES YET", "尚未更改"],
  ["PARTIAL FAILURE", "部分失败"],
  ["LOADING", "加载中"],
  ["LOCAL", "本机"],
  ["WARN", "警告"],
  ["Starting", "正在启动"],
  ["Stopped", "已停止"],
  ["A device-bound six-digit rotation code will be issued. The current token stays valid until the Deck redeems the code.", "将签发一个绑定设备的六位轮换码。在 Deck 兑换该码之前，当前 Token 仍然有效。"],
  ["The new device token appears only once in the authenticated Deck redeem response.", "新的设备 Token 只会在已认证的 Deck 兑换响应中出现一次。"],
  ["Reconnecting requires a fresh Setup AP pairing flow. Wi-Fi and device settings are preserved.", "重新连接需要重新执行 Setup AP 配对流程，Wi-Fi 与设备设置会保留。"],
  ["Only Wi-Fi candidate, marker and slots are removed. Calibration and Companion Profiles remain.", "只会移除 Wi-Fi 候选配置、标记和存储槽；校准与 Companion 配置档案会保留。"],
  ["The new slot is valid only after display, peripherals, Wi-Fi and Companion health checks pass.", "只有显示屏、外设、Wi-Fi 和 Companion 健康检查全部通过后，新分区才会生效。"],
  ["Management Web and Device Hub listeners will close. Connected Decks enter offline handling and retain their last valid snapshots.", "管理 Web 与 Device Hub 监听器将关闭。已连接的 Deck 会进入离线处理并保留最后一次有效快照。"],
  ["Provider configurations, credentials, device trust and serial presets are not affected.", "Provider 配置、凭据、设备信任和串口预设不受影响。"],
  ["Do not paste this code into chat, logs or issue evidence.", "不要把此配对码粘贴到聊天、日志或 Issue 证据中。"],
  ["Type CLEAR HISTORY to confirm. This cannot be undone.", "输入 CLEAR HISTORY 进行确认。此操作无法撤销。"],
  ["Type REVOKE to confirm.", "输入 REVOKE 进行确认。"],
  ["This confirmation token expires in 00:58.", "此确认 Token 将在 00:58 后过期。"],
  ["3,148 normalized snapshots will be deleted", "将删除 3,148 条规范化快照"],
  ["0.1.0 remains in ota_0", "0.1.0 将保留在 ota_0"],
  ["Pair a new Deck", "配对新 Deck"],
  ["Rotate Deck token", "轮换 Deck Token"],
  ["No plaintext token is displayed here", "此处不会显示明文 Token"],
  ["Issue rotation code", "签发轮换码"],
  ["Revoke device trust?", "撤销设备信任？"],
  ["This Deck will disconnect immediately", "此 Deck 将立即断开连接"],
  ["Revoke trust", "撤销信任"],
  ["Clear saved Wi-Fi?", "清除已保存的 Wi-Fi？"],
  ["Deck will remain in Setup Mode", "Deck 将保持在 Setup Mode"],
  ["Keep Wi-Fi", "保留 Wi-Fi"],
  ["Clear Wi-Fi", "清除 Wi-Fi"],
  ["Install signed firmware", "安装已签名固件"],
  ["Deck may reboot more than once", "Deck 可能会重启多次"],
  ["Install 0.2.0", "安装 0.2.0"],
  ["Add structured HTTP Provider", "添加结构化 HTTP Provider"],
  ["JSONPath mapping", "JSONPath 映射"],
  ["Secret reference", "密钥引用"],
  ["Polling", "轮询"],
  ["Save Provider", "保存 Provider"],
  ["Stop Companion?", "停止 Companion？"],
  ["Clear all usage history?", "清空全部用量历史？"],
  ["Clear 3,148 snapshots", "清除 3,148 条快照"],
  ["Keep history", "保留历史"],
  ["Prototype action", "原型操作"],
  ["Copy address + code", "复制地址与配对码"],
  ["Expires in", "剩余有效时间"],
  ["Single use", "仅可使用一次"],
  ["Confirmation", "确认内容"],
  ["Cancel", "取消"],
  ["File", "文件"],
  ["Signature", "签名"],
  ["Image hash", "镜像 Hash"],
  ["Rollback", "回滚"],
  ["ESP32-S3-RLCD-4.2 · match", "ESP32-S3-RLCD-4.2 · 匹配"],
  ["Ed25519 · valid", "Ed25519 · 有效"],
  ["SHA-256 · match", "SHA-256 · 匹配"],
  ["0.1.0 → 0.2.0 · SIGNED", "0.1.0 → 0.2.0 · 已签名"],
  ["18 seconds ago", "18 秒前"],
  ["ADD AI PROVIDER", "添加 AI Provider"],
  ["Active", "当前"],
  ["Companion profiles", "Companion 配置档案"],
  ["Closing AP in 08s", "AP 将在 08s 后关闭"],
  ["LAST SNAPSHOT", "上次快照"],
  ["NO PROFILE PREEMPTION", "配置档案不抢占"],
  ["Next", "下一项"],
  ["Open Setup / Recovery", "打开 Setup / 恢复"],
  ["RECOVERY", "恢复"],
  ["Reconnect Active Companion", "重新连接当前 Companion"],
  ["Reset", "重置"],
  ["Retry", "重试"],
  ["Setup complete", "Setup 完成"],
  ["Step 2 of 3", "第 2 步，共 3 步"],
  ["Step 3 of 3", "第 3 步，共 3 步"],
  ["Six-digit code", "六位配对码"],
  ["UART ERR", "UART 错误"],
  ["UART OVERFLOW", "UART 溢出"],
  ["Open Setup", "打开 Setup"],
  ["Deck is offline", "Deck 已离线"],
  ["SESSION /", "会话 /"],
  ["Preset name", "预设名称"],
  ["schema 1 · encrypted", "Schema 1 · 已加密"],
  ["Device link", "设备链路"],
  ["Import Provider only", "仅导入 Provider"],
  ["LAN management", "LAN 管理访问"],
  ["Provider order", "Provider 顺序"],
  ["Clear logs", "清空日志"],
  ["Estimated size 148 KiB.", "预估大小 148 KiB。"],
  ["Close this tab or wait for the local cooldown. Tokens are never logged.", "关闭此标签页或等待本机冷却结束。Token 永远不会写入日志。"],
  ["Import Providers only", "仅导入 Provider"],
  ["Resolve OpenRouter", "解决 OpenRouter 冲突"],
  ["Resolve Provider order", "解决 Provider 顺序冲突"],
  ["Resolve LAN management", "解决 LAN 管理访问冲突"],
  ["Code, module or event", "代码、模块或事件"],
  ["Hub host:port", "Hub 主机:端口"],
  ["Active Companion: this Mac", "当前 Companion：本机"],
  ["Provider API keys never visible", "Provider API 密钥绝不显示"],
  ["Web TX lease acquired", "已取得 Web TX 租约"],
  ["Web TX lease released", "已释放 Web TX 租约"],
  ["This browser may transmit for 10 minutes; USB input is rejected.", "此浏览器可发送 10 分钟；USB 输入将被拒绝。"],
  ["TX ownership returned to USB.", "TX 所有权已归还 USB。"],
  ["Bytes sent", "字节已发送"],
  [" characters accepted by the mock Web TX path.", " 个字符已由模拟 Web TX 路径接受。"],
  ["Only the current in-memory serial session would be included.", "仅包含当前内存中的串口会话。"],
  ["Network scan complete", "网络扫描完成"],
  ["3 networks found; results refreshed without exposing saved passwords.", "已发现 3 个网络；结果已刷新，不会暴露已保存的密码。"],
  ["Console access granted", "控制台访问已授权"],
  ["Single-use desktop grant exchanged for a local session.", "桌面端一次性授权已兑换为本机会话。"],
  ["Pairing details copied", "配对信息已复制"],
  ["Code expires in five minutes and may be redeemed once.", "配对码将在五分钟后过期，且只能兑换一次。"],
  ["Rotation code issued", "轮换码已签发"],
  ["Test request succeeded", "测试请求成功"],
  ["Provider saved", "Provider 已保存"],
  ["Device trust revoked", "设备信任已撤销"],
  ["Wi-Fi cleared; Setup remains active", "Wi-Fi 已清除；Setup 仍保持开启"],
  ["Usage history cleared", "用量历史已清空"],
  ["Companion stopped", "Companion 已停止"],
  ["Provider-only import prepared", "仅导入 Provider 的操作已准备"],
  ["Action completed", "操作已完成"],
  ["Destructive mock action completed after confirmation.", "已确认并完成模拟的危险操作。"],
  ["Mock action completed.", "模拟操作已完成。"],
  ["Snapshot refreshed", "快照已刷新"],
  ["All mock Provider and Deck timestamps are current.", "所有模拟 Provider 与 Deck 的时间戳均已更新。"],
  ["Diagnostics bundle ready", "诊断包已就绪"],
  ["Manifest and SHA-256 included; sensitive fields redacted.", "已包含清单与 SHA-256；敏感字段均已脱敏。"],
  ["Deck confirmed calibration", "Deck 已确认校准"],
  ["Temperature offset −4.0°C was written through and acknowledged.", "温度偏移 −4.0°C 已直写并获得确认。"],
  ["Local logs cleared", "本机日志已清空"],
  ["Provider history and device settings were preserved.", "Provider 历史与设备设置均已保留。"],
  ["No update available", "暂无可用更新"],
  ["Stable channel is current.", "当前已是稳定通道的最新版本。"],
  ["Import preview opened", "导入预览已打开"],
  ["cURL was parsed as data; no command was executed.", "cURL 已按数据解析，未执行任何命令。"],
  ["Deck order preview", "Deck 顺序预览"],
  ["Drag-and-drop ordering would be saved only after confirmation.", "拖放后的顺序只会在确认后保存。"],
  ["Request, mappings and display order passed validation.", "请求、映射与显示顺序均已通过验证。"],
  ["Redacted JSON copied", "脱敏 JSON 已复制"],
  ["Credential fields and account identifiers remain masked.", "凭据字段与账户标识仍保持遮盖。"],
  ["Mapping row added", "映射行已添加"],
  ["Select a normalized field and enter a JSONPath.", "请选择规范化字段并输入 JSONPath。"],
  ["CSV export ready", "CSV 导出已就绪"],
  ["Only normalized hourly metrics are included.", "仅包含规范化的小时级指标。"],
  ["Preset editor updated", "预设编辑器已更新"],
  ["No bytes were sent; Web TX ownership is still required.", "未发送任何字节；仍需取得 Web TX 所有权。"],
  ["Deck confirmed settings", "Deck 已确认设置"],
  ["Device-owned values were acknowledged and are now active.", "设备自有值已确认并生效。"],
  ["Encrypted backup created", "加密备份已创建"],
  ["The age archive passed its local integrity check.", "age 归档已通过本机完整性检查。"],
  ["Quit requested", "已请求退出"],
  ["The real shell performs bounded listener shutdown first.", "正式客户端会先按边界关闭监听器。"],
  ["Update channel changed", "更新通道已更改"],
  ["Stable is now selected.", "已选择稳定通道。"],
  ["Preview is now selected.", "已选择预览通道。"],
  ["Manual file only is now selected.", "已选择仅手动文件。"],
  [" is now selected.", " 已选中。"],
  ["Mock state updated successfully.", "模拟状态已成功更新。"],
  [" test succeeded", " 测试成功"],
  ["HTTP 200 · 184 ms · schema accepted.", "HTTP 200 · 184 ms · Schema 已接受。"],
  ["Firmware installed", "固件已安装"],
  ["Deck passed health confirmation; rollback slot preserved.", "Deck 已通过健康确认；回滚分区已保留。"],
  ["Web TX lease", "Web TX 租约"],
];

uiTextReplacements.sort((left, right) => right[0].length - left[0].length);

function translateUICopy(value) {
  let translated = value;
  for (const [source, target] of uiTextReplacements) translated = translated.replaceAll(source, target);
  translated = translated.replaceAll("AUTH_已过期", "AUTH_STALE");
  translated = translated.replaceAll("REV正常E", "REVOKE");
  translated = translated.replaceAll("本机_READ_FAILED", "LOCAL_READ_FAILED");
  translated = translated.replaceAll("本机_AUTH_RATE_LIMITED", "LOCAL_AUTH_RATE_LIMITED");
  return translated;
}

function localizeVisibleUI(root) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const textNodes = [];
  while (walker.nextNode()) textNodes.push(walker.currentNode);
  for (const node of textNodes) node.nodeValue = translateUICopy(node.nodeValue);
  for (const element of root.querySelectorAll("[aria-label], [title], [placeholder]")) {
    for (const attribute of ["aria-label", "title", "placeholder"]) {
      if (element.hasAttribute(attribute)) element.setAttribute(attribute, translateUICopy(element.getAttribute(attribute)));
    }
  }
}

const app = document.querySelector("#app");

function icon(name, className = "") {
  return `<svg class="icon ${className}" aria-hidden="true"><use href="#i-${name}"></use></svg>`;
}

function escapeHTML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function status(label, tone = "neutral") {
  return `<span class="status-pill ${tone}">${escapeHTML(label)}</span>`;
}

function brandLockup(compact = false) {
  return `<div class="brand-lockup"><div class="brand-mark" aria-hidden="true"><span></span><span></span><span></span><span></span></div>${compact ? "" : `<div class="brand-copy"><p class="brand-name">S3 RLCD Deck</p><div class="brand-meta">Companion v0.1.0</div></div>`}</div>`;
}

function currentPage() {
  return allPages.find((page) => page.id === state.page) || allPages[0];
}

function currentGroup() {
  return pageGroups.find((group) => group.items.some((item) => item.id === state.page)) || pageGroups[0];
}

function navButton(item, extraClass = "nav-item") {
  const active = state.page === item.id ? " active" : "";
  return `<button class="${extraClass}${active}" type="button" data-action="navigate" data-page="${item.id}" aria-current="${active ? "page" : "false"}">${icon(item.icon)}<span>${item.label}</span></button>`;
}

function mobileMenuButton() {
  return `<button class="icon-button mobile-menu-button" type="button" data-action="mobile-nav" aria-label="打开导航">${icon("menu")}</button>`;
}

function topbarActions() {
  const states = [["default", "Default"], ["loading", "Loading"], ["empty", "Empty"], ["error", "Error"]];
  return `<div class="topbar-tools"><label class="review-state"><span>Review state</span><select class="select" data-action="view-state" aria-label="切换页面评审状态">${states.map(([id, label]) => `<option value="${id}"${state.viewState === id ? " selected" : ""}>${label}</option>`).join("")}</select></label>${status("Companion ready", "success")}${status("1 Deck online", "info")}<button class="icon-button" type="button" data-action="toggle-state" aria-label="查看原型状态">${icon("eye")}</button><div class="avatar" aria-label="当前用户">XW</div></div>`;
}

function renderVariantC(content) {
  const group = currentGroup();
  return `<div class="c-layout"><nav class="c-dock" aria-label="领域导航">${brandLockup(true)}${pageGroups.map((item) => `<button class="dock-button${group.id === item.id ? " active" : ""}" type="button" data-action="navigate" data-page="${item.items[0].id}" aria-label="${item.label}" title="${item.label}">${icon(item.icon, "icon-lg")}</button>`).join("")}<div class="c-dock-footer"><button class="dock-button" type="button" data-action="navigate" data-page="login" aria-label="Access / Login" title="Access / Login">${icon("user", "icon-lg")}</button></div></nav><aside class="c-context" aria-label="上下文导航">${brandLockup()}<div class="context-kicker">Current module</div><div class="context-heading"><span>${icon(group.icon)}</span><div><strong>${group.label}</strong><small>${group.items.length} ${group.items.length === 1 ? "surface" : "surfaces"}</small></div></div><div class="context-title">Tasks</div><nav>${group.items.map((item) => navButton(item)).join("")}</nav><div class="context-status"><strong class="tone-success">SYSTEM NOMINAL</strong><span>1 Deck online · Loopback only</span><div class="context-pulse"><i></i><span>Last health check 4s ago</span></div></div></aside><div class="c-workspace"><header class="c-topbar"><div class="inline-cluster">${mobileMenuButton()}<div class="breadcrumbs"><span>Instrument</span>${icon("chevron", "icon-sm")}<span>${group.label}</span>${icon("chevron", "icon-sm")}<strong>${currentPage().label}</strong></div></div>${topbarActions()}</header><main class="c-content" id="prototype-main" tabindex="-1">${content}</main></div></div>`;
}

function pageHeading(eyebrow, title, description, actions = "") {
  return `<div class="page-heading"><div><div class="eyebrow">${eyebrow}</div><h1>${title}</h1><p>${description}</p></div>${actions ? `<div class="heading-actions">${actions}</div>` : ""}</div>`;
}

function metricCard(label, value, detail) {
  return `<article class="metric-card"><div class="metric-label">${label}</div><div class="metric-value">${value}</div><div class="metric-detail">${detail}</div></article>`;
}

function definition(term, value) {
  return `<div class="definition-row"><dt>${term}</dt><dd>${value}</dd></div>`;
}

function lineChart() {
  return `<div class="chart" role="img" aria-label="过去七日 Token 使用趋势"><svg viewBox="0 0 600 170" preserveAspectRatio="none"><defs><linearGradient id="chart-gradient" x1="0" x2="0" y1="0" y2="1"><stop offset="0" stop-color="currentColor"/><stop offset="1" stop-color="currentColor" stop-opacity="0"/></linearGradient></defs><path class="chart-fill" d="M0 135 C45 120 60 96 100 104 S160 128 200 112 S260 46 300 67 S355 88 400 70 S480 23 520 49 S570 62 600 42 L600 170 L0 170Z"/><path class="chart-path" d="M0 135 C45 120 60 96 100 104 S160 128 200 112 S260 46 300 67 S355 88 400 70 S480 23 520 49 S570 62 600 42"/><circle class="chart-marker" cx="600" cy="42" r="5"/></svg></div>`;
}

function providerSummary(initials, name, providerStatus, value, detail, tone, percent) {
  return `<article class="provider-card"><div class="provider-card-header"><div class="provider-title"><div class="avatar">${initials}</div><div><h3>${name}</h3><p>Provider snapshot</p></div></div>${status(providerStatus, tone)}</div><div class="provider-metric"><strong>${value}</strong><div class="progress ${tone === "warning" ? "warning" : ""}" style="--progress:${percent}%"><span></span></div></div><div class="provider-card-footer"><span>${detail}</span><button class="icon-button" type="button" data-action="provider-details" data-provider="${name}" aria-label="查看 ${name} 详情">${icon("chevron", "icon-sm")}</button></div></article>`;
}

function overviewPage() {
  return `${pageHeading("Command center", "Overview", "快速确认 Companion、Deck、Provider 和最近会话是否健康；异常项提供明确的下一步。", `<button class="button" data-action="refresh">${icon("refresh")}刷新快照</button><button class="button primary" data-action="navigate" data-page="providers">管理 Provider${icon("arrow")}</button>`)}<section class="metric-grid" aria-label="运行摘要">${metricCard("Connected Decks", "1", "Desk Deck · heartbeat 4s ago")}${metricCard("Healthy Providers", "3 / 4", "Cursor marked STALE")}${metricCard("Today Tokens", "132.4k", "+8.2% from 7-day average")}${metricCard("Serial Session", "DISARMED", "Target TX remains high-impedance")}</section><section class="section-block grid-2"><article class="surface-card chart-card"><div class="card-header"><div><h2>Token activity</h2><p>Normalized hourly totals · no prompt content stored</p></div>${status("7 days")}</div><div class="card-body">${lineChart()}<div class="card-footer"><span class="helper">Peak: 149k on Saturday</span><button class="button small ghost" data-action="navigate" data-page="sessions">Open sessions${icon("arrow", "icon-sm")}</button></div></div></article><article class="surface-card"><div class="card-header"><div><h2>Desk Deck</h2><p>Active device · S3-RLCD-A17F</p></div>${status("ONLINE", "success")}</div><div class="card-body"><dl class="definition-list">${definition("AI Page", "Codex · VERIFIED")}${definition("Network", "Studio-2G · −52 dBm")}${definition("Temperature", "24.8°C · calibrated")}${definition("Firmware", "0.1.0-dev+8f2a")}${definition("Active Companion", "Studio Mac · 4s ago")}</dl></div><div class="card-footer"><span class="helper">Last snapshot 10:36:18</span><button class="button small" data-action="navigate" data-page="deck">Preview screen</button></div></article></section><section class="section-block"><div class="section-title"><div><h2>Provider health</h2><p>Unavailable never appears as numeric zero.</p></div><button class="button small ghost" data-action="navigate" data-page="providers">View all</button></div><div class="provider-grid">${providerSummary("CX", "Codex", "VERIFIED", "62% left", "Resets in 02:44", "success", 62)}${providerSummary("CU", "Cursor", "STALE", "$18.42", "Updated 47m ago", "warning", 71)}${providerSummary("DS", "DeepSeek", "VERIFIED", "¥82.16", "Updated 18s ago", "success", 48)}</div></section>`;
}

const providers = [
  { initials: "CX", name: "Codex", type: "Local app server", state: "VERIFIED", tone: "success", metric: "62% left", updated: "18s ago", order: 1 },
  { initials: "CU", name: "Cursor", type: "Experimental read-only", state: "STALE", tone: "warning", metric: "$18.42", updated: "47m ago", order: 2 },
  { initials: "AH", name: "AIHubMix", type: "HTTPS template", state: "VERIFIED", tone: "success", metric: "$27.80", updated: "26s ago", order: 3 },
  { initials: "DS", name: "DeepSeek", type: "HTTPS template", state: "VERIFIED", tone: "success", metric: "¥82.16", updated: "18s ago", order: 4 },
  { initials: "IN", name: "Intranet LLM", type: "Private HTTP", state: "UNAVAILABLE", tone: "danger", metric: "—", updated: "Retry in 44s", order: 5 },
];

function toggleRow(title, description, active) {
  return `<div class="toggle-row"><div class="toggle-copy"><strong>${title}</strong><span>${description}</span></div><button class="switch${active ? " active" : ""}" type="button" aria-label="切换 ${title}" aria-pressed="${active}"></button></div>`;
}

function providerCard(provider) {
  const testLabel = state.providerTest === provider.name ? "Testing…" : "Test request";
  return `<article class="provider-card"><div class="provider-card-header"><div class="provider-title"><div class="avatar">${provider.initials}</div><div><h3>${provider.name}</h3><p>#${provider.order} · ${provider.type}</p></div></div>${status(provider.state, provider.tone)}</div><div class="provider-metric"><strong>${provider.metric}</strong><span class="helper">Updated ${provider.updated}</span></div><div class="provider-card-footer"><span>${provider.name === "Codex" ? "No secret required" : "Secret stored in OS vault"}</span></div><div class="button-row" style="margin-top:14px"><button class="button small" data-action="test-provider" data-provider="${provider.name}" ${state.providerTest ? "disabled" : ""}>${icon("refresh", "icon-sm")}${testLabel}</button><button class="button small ghost" data-action="edit-provider" data-provider="${provider.name}">Edit</button></div></article>`;
}

function providersPage() {
  const visible = providers.filter((provider) => state.providerFilter === "all" || provider.state.toLowerCase() === state.providerFilter);
  return `${pageHeading("Collection", "AI Providers", "配置数据源、凭据引用、展示顺序和轮询频率；单个 Provider 失败不会影响其他模块。", `<button class="button" data-action="import-provider">${icon("upload")}从 cURL 导入</button><button class="button primary" data-action="add-provider">添加 Provider</button>`)}<div class="callout warning">${icon("warning", "icon-lg")}<div><strong>Intranet LLM 使用私网 HTTP</strong><span>仅允许 RFC1918 地址。请求不会执行 Shell、JavaScript 或任意插件代码。</span></div></div><div class="section-block filter-row" aria-label="Provider 筛选">${["all", "verified", "stale", "unavailable"].map((filter) => `<button class="chip${state.providerFilter === filter ? " active" : ""}" type="button" data-action="provider-filter" data-filter="${filter}">${filter.toUpperCase()}</button>`).join("")}<div style="flex:1"></div><button class="button small" data-action="reorder-providers">${icon("session", "icon-sm")}调整 Deck 顺序</button></div><section class="section-block provider-grid">${visible.map(providerCard).join("") || emptyState("spark", "没有匹配的 Provider", "换一个筛选条件，或者添加新的数据源。")}</section><section class="section-block grid-2"><article class="surface-card"><div class="card-header"><div><h2>Polling & isolation</h2><p>刷新节奏和失败隔离</p></div></div><div class="card-body">${toggleRow("失败退避", "指数退避，最长 15 分钟", true)}${toggleRow("保存小时历史", "保留 90 天；不保存原始响应", true)}${toggleRow("自动发现 Cursor", "只读，不刷新 OAuth", true)}</div></article><article class="surface-card"><div class="card-header"><div><h2>History storage</h2><p>过去 30 天的规范化数据</p></div>${status("18.4 MiB", "info")}</div><div class="card-body"><div class="bar-chart">${[68, 82, 42, 91, 75, 54, 88].map((height, index) => `<div class="bar-item"><div class="bar" style="height:${height * 1.45}px"></div><span>${["一", "二", "三", "四", "五", "六", "日"][index]}</span></div>`).join("")}</div></div><div class="card-footer"><span class="helper">Oldest record · 2026-05-16</span><button class="button small">Export CSV</button></div></article></section>`;
}

function panelTabs(items, active, action) {
  return `<div class="panel-tabs" role="tablist">${items.map(([id, label]) => `<button class="panel-tab${active === id ? " active" : ""}" role="tab" aria-selected="${active === id}" data-action="${action}" data-tab="${id}">${label}</button>`).join("")}</div>`;
}

function providerEditorPage() {
  const tabs = [["request", "Request"], ["mapping", "Data mapping"], ["preview", "Test preview"], ["security", "Security"]];
  let body = "";
  if (state.providerEditorTab === "mapping") {
    body = `<div class="editor-grid"><div class="stack"><article class="surface-card"><div class="card-header"><div><h2>Normalized fields</h2><p>Map only fields that are present; unknown values stay null.</p></div>${status("Schema v1", "info")}</div><div class="card-body"><div class="mapping-list">${[["Balance", "$.data.balance", "18.42"], ["Currency", "$.data.currency", "USD"], ["Used percent", "$.data.usage_percent", "71"], ["Reset time", "$.data.resets_at", "2026-08-16T00:00:00Z"]].map(([label, path, sample]) => `<div class="mapping-row"><div><strong>${label}</strong><span>Optional normalized field</span></div><input class="input mono" value="${path}" aria-label="${label} JSONPath"><code>${sample}</code></div>`).join("")}</div><button class="button" data-action="add-mapping">${icon("spark")}Add mapping</button></div></article></div><aside class="surface-card"><div class="card-header"><div><h2>Transform rules</h2><p>Declarative only—no scripts.</p></div></div><div class="card-body"><label class="field"><span>Number divisor</span><input class="input mono" value="1"></label><label class="field" style="margin-top:14px"><span>Timestamp format</span><select class="select"><option>ISO 8601</option><option>Unix seconds</option></select></label><div class="callout warning" style="margin-top:16px">${icon("warning")}<div><strong>Missing fields collapse</strong><span>The Deck must never display zero when the upstream value is unknown.</span></div></div></div></aside></div>`;
  } else if (state.providerEditorTab === "preview") {
    body = `<div class="editor-grid"><article class="surface-card"><div class="card-header"><div><h2>Redacted response preview</h2><p>HTTP 200 · 184 ms · 238 bytes</p></div>${status("VALID", "success")}</div><div class="card-body"><pre class="code-panel">{
  "data": {
    "balance": 18.42,
    "currency": "USD",
    "usage_percent": 71,
    "account": "••••@example.com"
  }
}</pre><div class="button-row" style="margin-top:16px"><button class="button primary" data-action="test-provider" data-provider="OpenRouter">${icon("refresh")}Run test again</button><button class="button" data-action="copy-preview">${icon("copy")}Copy redacted JSON</button></div></div></article><aside class="surface-card"><div class="card-header"><div><h2>Deck snapshot</h2><p>What the device is allowed to receive</p></div></div><div class="card-body"><dl class="definition-list">${definition("Provider", "OpenRouter")}${definition("Status", "VERIFIED")}${definition("Balance", "$18.42")}${definition("Usage", "71%")}${definition("Reset", "2026-08-16")}${definition("Secret", "NEVER SENT")}</dl></div></aside></div>`;
  } else if (state.providerEditorTab === "security") {
    body = `<div class="grid-2"><article class="surface-card"><div class="card-header"><div><h2>Transport boundary</h2><p>Validated before every request.</p></div>${status("HTTPS", "success")}</div><div class="card-body">${toggleRow("Block cross-host redirects", "Redirects to another hostname fail closed", true)}${toggleRow("Limit response body", "Hard limit 256 KiB", true)}${toggleRow("Redact diagnostic preview", "Headers and mapped secrets are masked", true)}<div class="callout" style="margin-top:16px">${icon("shield")}<div><strong>10 second request timeout</strong><span>No shell, JavaScript, arbitrary plugin or executable cURL is accepted.</span></div></div></div></article><article class="surface-card"><div class="card-header"><div><h2>Credential</h2><p>Stored by the operating system vault.</p></div>${status("AVAILABLE", "success")}</div><div class="card-body"><dl class="definition-list">${definition("Reference", "keychain://s3deck/openrouter")}${definition("Created", "2026-08-13 09:12")}${definition("Last used", "18 seconds ago")}${definition("Export", "Included only in encrypted backup")}</dl><div class="button-row" style="margin-top:18px"><button class="button">Replace credential</button><button class="button danger">Remove credential</button></div></div></article></div>`;
  } else {
    body = `<div class="editor-grid"><article class="surface-card"><div class="card-header"><div><h2>HTTP request</h2><p>Structured configuration, never an executable command.</p></div>${status("ENABLED", "success")}</div><div class="card-body"><form class="form-grid"><label class="field"><span>Display name</span><input class="input" value="OpenRouter"></label><label class="field"><span>Template</span><select class="select"><option>Custom HTTPS JSON</option><option>AIHubMix</option><option>DeepSeek</option></select></label><label class="field"><span>Method</span><select class="select"><option>GET</option><option>POST</option></select></label><label class="field"><span>Polling interval</span><select class="select"><option>Every 5 minutes</option><option>Every 15 minutes</option><option>Every hour</option></select></label><label class="field full"><span>Endpoint</span><input class="input mono" type="url" value="https://openrouter.ai/api/v1/credits"></label><label class="field full"><span>Headers</span><textarea class="textarea mono">Authorization: Bearer {{credential}}&#10;Accept: application/json</textarea><small>Credential value is injected from the OS vault and never shown again.</small></label><label class="field full"><span>JSON body</span><textarea class="textarea mono" placeholder="Optional for POST requests"></textarea></label></form></div></article><aside class="stack"><article class="surface-card"><div class="card-header"><div><h2>Deck order</h2><p>Page 5 of 5</p></div></div><div class="card-body"><label class="field"><span>Display order</span><input class="input" type="number" min="1" value="5"></label>${toggleRow("Show on Deck", "Included in KEY page cycle", true)}</div></article><div class="callout warning">${icon("warning")}<div><strong>Remote endpoints require HTTPS</strong><span>Plain HTTP is accepted only for private RFC1918 addresses and always shows a warning.</span></div></div></aside></div>`;
  }
  return `${pageHeading("Provider workspace", "Edit OpenRouter", "Configure request, mappings, redacted validation and credential boundaries as one inspectable workflow.", `<button class="button" data-action="navigate" data-page="providers">Back to Providers</button><button class="button" data-action="test-provider" data-provider="OpenRouter">Test request</button><button class="button primary" data-action="save-provider">Save changes</button>`)}${panelTabs(tabs, state.providerEditorTab, "provider-editor-tab")}<section class="section-block">${body}</section>`;
}

function historyPage() {
  const daily = [["Aug 15", "132.4k", "$2.14", "4"], ["Aug 14", "118.9k", "$1.82", "4"], ["Aug 13", "149.1k", "$2.48", "5"], ["Aug 12", "84.7k", "$1.20", "4"], ["Aug 11", "102.2k", "$1.62", "3"]];
  return `${pageHeading("Local history", "Usage History", "浏览本机保存的小时级规范化快照；不保存 Prompt、回复、原始 Provider 响应或串口正文。", `<button class="button" data-action="export-history">${icon("download")}Export CSV</button><button class="button danger" data-action="clear-history">Clear history…</button>`)}<section class="metric-grid">${metricCard("7-day tokens", "824.6k", "+8.2% vs previous period")}${metricCard("Estimated spend", "$12.86", "Across 3 priced Providers")}${metricCard("Snapshots", "3,148", "Hourly normalized rows")}${metricCard("Retention", "90 days", "Oldest record · May 16")}</section><div class="filter-row section-block"><div class="segmented">${["24h", "7d", "30d", "90d"].map((range) => `<button class="chip${state.historyRange === range ? " active" : ""}" data-action="history-range" data-range="${range}">${range}</button>`).join("")}</div><label class="field compact-field"><span>Provider</span><select class="select" data-action="history-provider"><option value="all">All Providers</option><option value="codex">Codex</option><option value="cursor">Cursor</option><option value="deepseek">DeepSeek</option></select></label></div><section class="grid-2 section-block"><article class="surface-card chart-card"><div class="card-header"><div><h2>Token activity</h2><p>Hourly total · ${state.historyRange}</p></div>${status(state.historyProvider === "all" ? "ALL PROVIDERS" : state.historyProvider.toUpperCase(), "info")}</div><div class="card-body">${lineChart()}<div class="chart-legend"><span><i class="legend-dot primary"></i>Input 62%</span><span><i class="legend-dot muted"></i>Cached 24%</span><span><i class="legend-dot warning"></i>Output + reasoning 14%</span></div></div></article><article class="surface-card"><div class="card-header"><div><h2>History controls</h2><p>Changes affect future snapshots.</p></div></div><div class="card-body">${toggleRow("Save usage history", "Hourly normalized snapshots on this computer", true)}${toggleRow("Automatic retention", "Delete rows older than 90 days", true)}${toggleRow("Store anonymous session totals", "No title, prompt, response or path", true)}<div class="callout" style="margin-top:16px">${icon("shield")}<div><strong>18.4 MiB local storage</strong><span>History database is deliberately excluded from encrypted configuration backups.</span></div></div></div></article></section><section class="section-block data-table-wrap"><table class="data-table"><thead><tr><th>Date</th><th>Tokens</th><th>Estimated spend</th><th>Providers</th><th>Confidence</th></tr></thead><tbody>${daily.map(([date, tokens, spend, count]) => `<tr><td><strong>${date}</strong></td><td class="mono">${tokens}</td><td class="mono">${spend}</td><td>${count}</td><td>${status("MIXED", "info")}</td></tr>`).join("")}</tbody></table></section>`;
}

const sessions = [
  { name: "s3-rlcd-deck", status: "RUNNING?", trust: "INFERRED", tone: "warning", duration: "12m", tokens: "146k", context: "41%", source: "JSONL observer" },
  { name: "payment-reconciliation", status: "RUNNING", trust: "VERIFIED", tone: "success", duration: "4m", tokens: "32k", context: "18%", source: "Companion-owned" },
  { name: "design-system-audit", status: "RECENT", trust: "INFERRED", tone: "info", duration: "Ended 8m ago", tokens: "89k", context: "—", source: "Process observer" },
  { name: "thread-a91f", status: "ENDED", trust: "VERIFIED", tone: "neutral", duration: "Ended 1h ago", tokens: "51k", context: "—", source: "Companion-owned" },
];

function sessionsPage() {
  const visible = sessions.filter((session) => state.sessionFilter === "all" || session.trust.toLowerCase() === state.sessionFilter);
  return `${pageHeading("Privacy-safe activity", "Codex Sessions", "仅显示脱敏名称、可信度、时间和匿名统计；Prompt、回复、工具参数与绝对路径不会进入此页。", `<button class="button">${icon("download")}Export anonymous CSV</button>`)}<div class="filter-row">${["all", "verified", "inferred"].map((filter) => `<button class="chip${state.sessionFilter === filter ? " active" : ""}" data-action="session-filter" data-filter="${filter}">${filter.toUpperCase()}</button>`).join("")}<label class="field" style="margin-left:auto;min-width:min(100%,260px)"><span class="helper">Search sessions</span><input class="input" type="search" placeholder="Search redacted name"></label></div><section class="section-block data-table-wrap"><table class="data-table"><thead><tr><th>Session</th><th>State</th><th>Confidence</th><th>Duration</th><th>Tokens</th><th>Context</th><th>Source</th></tr></thead><tbody>${visible.map((session) => `<tr><td><strong>${session.name}</strong></td><td>${session.status}</td><td>${status(session.trust, session.tone)}</td><td>${session.duration}</td><td class="mono">${session.tokens}</td><td class="mono">${session.context}</td><td>${session.source}</td></tr>`).join("")}</tbody></table></section><section class="section-block grid-2"><article class="surface-card chart-card"><div class="card-header"><div><h2>Session token trend</h2><p>Aggregated values only</p></div><div class="segmented">${["24h", "7d", "30d"].map((range) => `<button class="chip${state.historyRange === range ? " active" : ""}" data-action="history-range" data-range="${range}">${range}</button>`).join("")}</div></div><div class="card-body">${lineChart()}</div></article><article class="surface-card"><div class="card-header"><div><h2>Confidence semantics</h2><p>状态来源必须可解释</p></div></div><div class="card-body"><dl class="definition-list">${definition("VERIFIED", "Companion directly owns or observes state")}${definition("INFERRED", "Estimated from process or JSONL evidence")}${definition("UNAVAILABLE", "No trustworthy current evidence")}</dl><div class="callout warning" style="margin-top:16px">${icon("warning")}<div><strong>“RUNNING?” includes a question mark</strong><span>File activity alone cannot prove waiting approval or active generation.</span></div></div></div></article></section>`;
}

function terminalOutput() {
  if (state.serialMode === "hex") {
    return `<span class="dim">0001A4F0</span>  7E 00 12 43 4F 44 45 58 20 44 45 43 4B 0D 0A 7E<br><span class="dim">0001A500</span>  01 02 00 7F A4 2C 19 44 00 00 1A F0 0D 0A 00 00<br><span class="dim">0001A510</span>  54 45 4D 50 3D 32 34 2E 38 2C 48 55 4D 3D 34 36`;
  }
  if (state.serialMode === "mixed") {
    return `<span class="dim">10:35:49.018 RX</span> <span class="rx">boot: target ready</span> <span class="dim">[62 6F 6F 74 3A 20 74 61 72 67 65 74 20 72 65 61 64 79]</span><br><span class="dim">10:35:50.102 RX</span> <span class="rx">temp=24.8,hum=46.2</span> <span class="dim">[74 65 6D 70 3D 32 34 2E 38 2C 68 75 6D 3D 34 36 2E 32]</span><br><span class="dim">10:35:52.004 TX</span> <span class="tx">status\\r\\n</span> <span class="dim">[73 74 61 74 75 73 0D 0A]</span>`;
  }
  return `<span class="dim">[10:35:48.922] session started · owner USB TX</span><br><span class="rx">boot: target ready</span><br><span class="rx">firmware: 2.7.1</span><br><span class="rx">temp=24.8,hum=46.2</span><br><span class="tx">status</span><br><span class="rx">uptime=1402 rx=12482109 tx=18420 errors=0</span><br><span class="dim">[10:36:18.000] heartbeat · raw bytes preserved</span>`;
}

function serialPage() {
  const canSend = state.webTx && !state.serialPaused;
  const modes = [{ id: "text", label: "Text / ANSI" }, { id: "hex", label: "HEX" }, { id: "mixed", label: "Text + HEX" }];
  return `${pageHeading("Bounded serial session", "Serial Terminal", "浏览器显示 Text/ANSI、HEX 或混合视图；原始字节始终由 Companion 与 Deck 保持，不经过 UI 转码。", `${status(state.webTx ? "WEB TX · 09:42" : "USB TX owner", state.webTx ? "warning" : "info")}<button class="button ${state.webTx ? "danger" : "primary"}" data-action="toggle-web-tx">${state.webTx ? "Release Web TX" : "Acquire Web TX"}</button>`)}<div class="callout ${state.webTx ? "warning" : ""}">${icon(state.webTx ? "warning" : "shield", "icon-lg")}<div><strong>${state.webTx ? "This browser owns Web TX" : "Serial input is owned by USB"}</strong><span>${state.webTx ? "USB input is rejected until the lease is released, this page closes, or the 10-minute lease expires." : "Multiple browsers may observe, but cannot send until one browser acquires the exclusive Web TX lease."}</span></div></div><section class="section-block serial-shell"><div class="serial-toolbar"><div class="terminal-tabs">${modes.map((mode) => `<button class="terminal-tab${state.serialMode === mode.id ? " active" : ""}" data-action="serial-mode" data-mode="${mode.id}">${mode.label}</button>`).join("")}</div><div class="inline-cluster">${status(state.serialPaused ? "PAUSED" : "LIVE", state.serialPaused ? "warning" : "success")}<button class="icon-button" data-action="pause-terminal" aria-label="${state.serialPaused ? "继续" : "暂停"}终端">${icon(state.serialPaused ? "refresh" : "pause")}</button><button class="icon-button" data-action="download-serial" aria-label="下载当前会话">${icon("download")}</button></div></div><div class="serial-main"><div class="terminal-canvas"><div class="terminal-output" aria-label="模拟串口输出">${terminalOutput()}</div><div class="terminal-compose"><input class="terminal-input" id="terminal-input" ${canSend ? "" : "disabled"} placeholder="${state.serialMode === "hex" ? "48 65 6C 6C 6F" : canSend ? "Send to target…" : "Acquire Web TX to send"}" aria-label="串口发送内容"><button class="button primary" data-action="send-serial" ${canSend ? "" : "disabled"}>Send</button></div></div><aside class="serial-inspector"><h3>SESSION / 00:18:32</h3><dl class="definition-list">${definition("Mode", "115200 · 8N1")}${definition("RX", "12,482,109 B")}${definition("TX", "18,420 B")}${definition("Rate", "11.3 KiB/s")}${definition("UART errors", "0")}${definition("ESP overwrite", "0")}${definition("Companion overwrite", "1,024 B")}${definition("Web clients", "2 read · 1 owner")}</dl><h3 style="margin-top:20px">LINE ENDING</h3><div class="segmented"><button class="chip active">CRLF</button><button class="chip">LF</button><button class="chip">None</button></div><h3 style="margin-top:20px">PRESETS</h3><div class="stack"><button class="button small">AT</button><button class="button small">Reset target</button><button class="button small">Version query</button></div></aside></div></section>`;
}

function serialPresetsPage() {
  const presets = [
    ["AT", "Text", "AT\\r\\n", "All devices"],
    ["Version query", "Text", "version\\r\\n", "Desk Deck"],
    ["Reset target", "HEX", "7E 01 02 00 7F", "Desk Deck"],
    ["Enter bootloader", "Text", "boot --safe\\r\\n", "Lab Deck"],
  ];
  const visible = state.presetFilter === "all" ? presets : presets.filter((preset) => preset[1].toLowerCase() === state.presetFilter);
  return `${pageHeading("Reusable commands", "Serial Presets", "保存结构化文本或 HEX 命令；预设只在明确取得 Web TX Lease 后发送，并沿用当前行结束设置。", `<button class="button primary" data-action="new-preset">${icon("spark")}New preset</button>`)}<div class="callout">${icon("shield", "icon-lg")}<div><strong>Presets do not bypass TX ownership</strong><span>They remain disabled while USB owns TX, the terminal is paused, or the Serial Session has ended.</span></div></div><div class="filter-row section-block">${["all", "text", "hex"].map((filter) => `<button class="chip${state.presetFilter === filter ? " active" : ""}" data-action="preset-filter" data-filter="${filter}">${filter.toUpperCase()}</button>`).join("")}<label class="field compact-field"><span>Search</span><input class="input" type="search" placeholder="Preset name"></label></div><section class="editor-grid section-block"><div class="data-table-wrap"><table class="data-table"><thead><tr><th>Name</th><th>Mode</th><th>Payload preview</th><th>Scope</th><th>Actions</th></tr></thead><tbody>${visible.map(([name, mode, payload, scope]) => `<tr><td><strong>${name}</strong></td><td>${status(mode.toUpperCase(), mode === "HEX" ? "warning" : "info")}</td><td><code>${payload}</code></td><td>${scope}</td><td><div class="button-row"><button class="button small" data-action="edit-preset" data-name="${name}">Edit</button><button class="button small ghost" disabled>Send</button></div></td></tr>`).join("")}</tbody></table></div><aside class="surface-card"><div class="card-header"><div><h2>Preset editor</h2><p>Unsaved example</p></div>${status("DRAFT", "warning")}</div><div class="card-body"><label class="field"><span>Name</span><input class="input" value="健康检查"></label><label class="field" style="margin-top:14px"><span>Mode</span><select class="select"><option>Text</option><option>HEX bytes</option></select></label><label class="field" style="margin-top:14px"><span>Payload</span><textarea class="textarea mono">health --compact</textarea></label><label class="field" style="margin-top:14px"><span>Line ending</span><select class="select"><option>Use current terminal setting</option><option>CRLF</option><option>LF</option><option>None</option></select></label><div class="button-row" style="margin-top:18px"><button class="button primary" data-action="save-preset">Save preset</button><button class="button">Clear</button></div></div></aside></section>`;
}

const deckInventory = {
  desk: { name: "Desk Deck", id: "deck_A17F…92C1", status: "ONLINE", tone: "success", last: "4s ago", wifi: "Studio-2G · −52 dBm", firmware: "0.1.0-dev+8f2a", companion: "Studio Mac", temp: "24.8°C", uart: "DISARMED" },
  lab: { name: "Lab Deck", id: "deck_42D0…B80A", status: "OFFLINE", tone: "warning", last: "8 days ago", wifi: "Lab-IoT · last known", firmware: "0.0.9", companion: "Lab Windows", temp: "Unavailable", uart: "DISARMED" },
};

function devicesPage() {
  const selected = deckInventory[state.selectedDevice];
  return `${pageHeading("Physical fleet", "Deck Inventory", "检查每块 Deck 的当前状态、设备拥有的配置和最近确认；修改必须等待目标 Deck 明确回执。", `<button class="button primary" data-action="pair-code">${icon("device")}Pair a Deck</button>`)}<section class="device-layout"><div class="device-list surface-card"><div class="card-header"><div><h2>Trusted Decks</h2><p>2 paired · 1 online</p></div></div><div class="device-list-body">${Object.entries(deckInventory).map(([id, deck]) => `<button class="device-row${state.selectedDevice === id ? " active" : ""}" data-action="select-device" data-device="${id}"><div class="device-glyph">${icon("chip", "icon-lg")}</div><div><strong>${deck.name}</strong><span>${deck.id}</span></div>${status(deck.status, deck.tone)}</button>`).join("")}</div><div class="card-footer"><span class="helper">Maximum 5 profiles per Deck</span></div></div><div class="device-detail stack"><article class="surface-card"><div class="card-header"><div class="identity-row"><div class="device-glyph large">${icon("chip", "icon-lg")}</div><div><h2>${selected.name}</h2><p>${selected.id}</p></div></div>${status(selected.status, selected.tone)}</div><div class="card-body"><div class="detail-metrics"><div><span>Last heartbeat</span><strong>${selected.last}</strong></div><div><span>Temperature</span><strong>${selected.temp}</strong></div><div><span>Serial</span><strong>${selected.uart}</strong></div></div><dl class="definition-list" style="margin-top:18px">${definition("Wi-Fi", selected.wifi)}${definition("Firmware", selected.firmware)}${definition("Active Companion", selected.companion)}${definition("AI Page", state.selectedDevice === "desk" ? "Codex · VERIFIED" : "Snapshot expired")}${definition("Protocol", "device-link v1")}</dl></div><div class="card-footer"><div class="button-row"><button class="button" data-action="navigate" data-page="deck">Preview RLCD</button><button class="button" data-action="navigate" data-page="network">Manage trust</button></div></div></article><article class="surface-card"><div class="card-header"><div><h2>Device-owned settings</h2><p>Write-through; success only after Deck acknowledgement.</p></div>${status(selected.status === "ONLINE" ? "AVAILABLE" : "READ ONLY", selected.status === "ONLINE" ? "success" : "warning")}</div><div class="card-body"><form class="form-grid"><label class="field"><span>UART preset</span><select class="select" ${selected.status !== "ONLINE" ? "disabled" : ""}><option>115200 · 8N1</option><option>9600 · 8N1</option></select></label><label class="field"><span>Screen refresh</span><select class="select" ${selected.status !== "ONLINE" ? "disabled" : ""}><option>1 second</option><option>2 seconds</option><option>5 seconds</option></select></label><label class="field"><span>Temperature offset</span><input class="input" value="−4.0°C" ${selected.status !== "ONLINE" ? "disabled" : ""}></label><label class="field"><span>Timezone cache</span><input class="input" value="Asia/Shanghai" ${selected.status !== "ONLINE" ? "disabled" : ""}></label></form>${selected.status === "ONLINE" ? `<button class="button primary" style="margin-top:18px" data-action="save-device">Save and wait for Deck</button>` : `<div class="callout warning" style="margin-top:18px">${icon("warning")}<div><strong>Deck is offline</strong><span>Cached settings are shown for inspection and will not overwrite the Deck when it reconnects.</span></div></div>`}</div></article></div></section>`;
}

function networkPage() {
  const warning = state.lanManagement ? `<div class="callout warning">${icon("warning", "icon-lg")}<div><strong>LAN management is enabled</strong><span>The full management Web is exposed beyond loopback. Login, Origin and CSRF checks remain required.</span></div></div>` : "";
  return `${pageHeading("Trust & connectivity", "Network", "管理 Companion 监听面、Deck 配对信任、连接健康和设备侧 Wi‑Fi；Provider 凭据不会进入 Deck。", `<button class="button primary" data-action="pair-code">${icon("device")}Pair a Deck</button>`)}${warning}<section class="section-block grid-2"><article class="surface-card"><div class="card-header"><div><h2>Companion listeners</h2><p>Management Web 与 Device Hub 严格分离</p></div>${status("READY", "success")}</div><div class="card-body"><dl class="definition-list">${definition("Management Web", state.lanManagement ? "0.0.0.0:7777" : "127.0.0.1:7777")}${definition("Device Hub", "192.168.1.20:7443 · TLS")}${definition("Certificate", "SHA-256 7B:42:…:9F")}${definition("Runtime", "v0.1.0 · 16h 08m")}</dl>${toggleRow("Allow LAN management", "默认关闭；启用后显示持续安全警告", state.lanManagement)}<button class="button small" data-action="toggle-lan">${state.lanManagement ? "Disable LAN access" : "Enable LAN access"}</button></div></article><article class="surface-card"><div class="card-header"><div><h2>Pairing workflow</h2><p>Setup AP 内完成一次性信任引导</p></div></div><div class="card-body"><div class="stack"><div class="callout">${icon("device")}<div><strong>1. Connect computer to Deck Setup AP</strong><span>S3-RLCD-A17F · random WPA2 password appears only on Deck.</span></div></div><div class="callout">${icon("copy")}<div><strong>2. Generate a six-digit code here</strong><span>The code expires in five minutes and can be redeemed once.</span></div></div><div class="callout">${icon("shield")}<div><strong>3. Enter Hub address and code on Deck Setup</strong><span>Deck pins the TLS certificate fingerprint before committing.</span></div></div></div></div><div class="card-footer"><span class="helper">No active pairing code</span><button class="button small primary" data-action="pair-code">Generate code</button></div></article></section><section class="section-block"><div class="section-title"><div><h2>Trusted Decks</h2><p>Token rotation and revocation require explicit confirmation.</p></div></div><div class="data-table-wrap"><table class="data-table"><thead><tr><th>Deck</th><th>Device ID</th><th>Status</th><th>Last heartbeat</th><th>Firmware</th><th>Actions</th></tr></thead><tbody><tr><td><strong>Desk Deck</strong><br><span class="helper">Active Companion: this Mac</span></td><td class="mono">deck_A17F…92C1</td><td>${status("ONLINE", "success")}</td><td>4s ago</td><td>0.1.0-dev</td><td><div class="button-row"><button class="button small" data-action="rotate-token">Rotate token</button><button class="button small danger" data-action="revoke-device">Revoke</button></div></td></tr><tr><td><strong>Lab Deck</strong></td><td class="mono">deck_42D0…B80A</td><td>${status("OFFLINE", "warning")}</td><td>8 days ago</td><td>0.0.9</td><td><div class="button-row"><button class="button small" data-action="rotate-token">Rotate token</button><button class="button small danger" data-action="revoke-device">Revoke</button></div></td></tr></tbody></table></div></section>`;
}

function otaProgressBlock() {
  return `<div style="margin-top:18px"><div class="inline-cluster" style="justify-content:space-between"><strong>Writing inactive slot</strong><span class="mono">${state.otaProgress}%</span></div><div class="progress" style="--progress:${state.otaProgress}%;margin-top:10px"><span></span></div><p class="helper" style="margin-top:10px">Signature, board and image hash verified. Do not disconnect power.</p></div>`;
}

function systemPage() {
  const ota = state.otaProgress ? otaProgressBlock() : `<div class="button-row" style="margin-top:18px"><button class="button primary" data-action="ota-update">${icon("upload")}Select firmware</button><button class="button" data-action="ota-check">Check for update</button></div>`;
  return `${pageHeading("Maintenance", "System", "设备时间、传感器、固件、OTA、脱敏诊断、备份和 Companion 生命周期集中在这里。", `<button class="button" data-action="export-diagnostics">${icon("download")}Export diagnostics</button>`)}<section class="metric-grid">${metricCard("Companion uptime", "16h 08m", "No unexpected restart")}${metricCard("Deck temperature", "24.8°C", "Offset −4.0°C")}${metricCard("Log storage", "12.6 MiB", "7 days · 50 MiB cap")}${metricCard("Firmware", "0.1.0-dev", "Update channel: stable")}</section><section class="section-block grid-2"><article class="surface-card"><div class="card-header"><div><h2>Deck firmware</h2><p>Signed A/B OTA with health confirmation</p></div>${status("UP TO DATE", "success")}</div><div class="card-body"><dl class="definition-list">${definition("Running slot", "ota_0 · VALID")}${definition("Rollback slot", "0.0.9 · READY")}${definition("Board", "ESP32-S3-RLCD-4.2")}${definition("Protocol", "device-link v1")}</dl>${ota}</div></article><article class="surface-card"><div class="card-header"><div><h2>Device calibration</h2><p>Only confirmed device-owned values show success</p></div></div><div class="card-body"><form class="form-grid"><label class="field"><span>Temperature offset</span><input class="input" type="number" min="-15" max="15" step="0.1" value="-4.0"><small>Allowed range −15.0°C to +15.0°C</small></label><label class="field"><span>Local time source</span><select class="select"><option>RTC + Companion sync</option><option>RTC only</option></select></label><div class="field full"><button class="button primary" type="button" data-action="save-calibration">Save and wait for Deck</button></div></form></div></article></section><section class="section-block grid-2"><article class="surface-card"><div class="card-header"><div><h2>Diagnostics & privacy</h2><p>All export fields are redacted before packaging</p></div>${status("NO TELEMETRY", "info")}</div><div class="card-body">${toggleRow("Rotating logs", "7 days or 50 MiB", true)}${toggleRow("Crash reports", "Never sent outside this computer", false)}${toggleRow("Include Deck memory ring", "No prompts, paths, secrets or serial body", true)}<div class="button-row" style="margin-top:16px"><button class="button" data-action="export-diagnostics">${icon("download")}Build diagnostics.zip</button><button class="button danger" data-action="clear-logs">Clear local logs</button></div></div></article><article class="surface-card"><div class="card-header"><div><h2>Encrypted backup</h2><p>Provider configuration and user-entered secrets only</p></div></div><div class="card-body"><div class="callout">${icon("shield")}<div><strong>Device trust is excluded</strong><span>Pairing tokens, Web sessions, history database and serial buffers are never exported.</span></div></div><div class="button-row" style="margin-top:18px"><button class="button">${icon("download")}Export .age backup</button><button class="button">${icon("upload")}Import & preview</button></div></div></article></section><section class="section-block surface-card"><div class="card-header"><div><h2>Companion lifecycle</h2><p>Local background service and startup behavior</p></div>${status("RUNNING", "success")}</div><div class="card-body">${toggleRow("Start at login", "macOS LaunchAgent / Windows Task Scheduler", true)}${toggleRow("Automatic update checks", "Download never starts without confirmation", true)}<div class="button-row" style="margin-top:16px"><button class="button">Restart Companion</button><button class="button danger" data-action="stop-companion">Stop Companion</button></div></div></section>`;
}

function updatesPage() {
  const ota = state.otaProgress ? otaProgressBlock() : `<div class="button-row"><button class="button primary" data-action="ota-update">${icon("upload")}Select signed firmware</button><button class="button" data-action="ota-check">Check stable channel</button></div>`;
  return `${pageHeading("Signed delivery", "Firmware Updates", "检查、验证、安装和回滚固件；新分区只有通过显示、外设、网络与 Companion 健康检查后才会生效。", status("NO SILENT UPDATES", "info"))}<section class="update-hero surface-card"><div><div class="eyebrow">Desk Deck · ota_0</div><h2>0.1.0-dev+8f2a</h2><p>Running image is valid. Rollback image 0.0.9 remains available in ota_1.</p><div class="button-row">${ota}</div></div><div class="firmware-orbit" aria-hidden="true"><div class="orbit-core">S3</div><span class="orbit-slot active">OTA 0<br>VALID</span><span class="orbit-slot">OTA 1<br>READY</span></div></section><section class="section-block grid-2"><article class="surface-card"><div class="card-header"><div><h2>Update policy</h2><p>User-confirmed, signed and board-specific.</p></div></div><div class="card-body"><label class="field"><span>Channel</span><select class="select" data-action="update-channel"><option value="stable">Stable</option><option value="preview">Preview</option><option value="manual">Manual file only</option></select></label>${toggleRow("Automatic checks", "Checks only; downloads need confirmation", true)}${toggleRow("Preserve rollback slot", "Required before installation", true)}${toggleRow("Confirm board model", "ESP32-S3-RLCD-4.2 must match", true)}</div></article><article class="surface-card"><div class="card-header"><div><h2>Health confirmation</h2><p>Required after first boot.</p></div>${status("4 / 4 PASS", "success")}</div><div class="card-body"><div class="check-list">${[["Display initialization", "PASS"], ["RTC + SHTC3", "PASS"], ["Wi-Fi recovery", "PASS"], ["Companion handshake", "PASS"]].map(([label, value]) => `<div><span>${icon("check")}${label}</span>${status(value, "success")}</div>`).join("")}</div><div class="callout warning" style="margin-top:16px">${icon("warning")}<div><strong>Any failure triggers rollback</strong><span>BOOT/UART flashing remains the last-resort recovery path.</span></div></div></div></article></section><section class="section-block surface-card"><div class="card-header"><div><h2>Update history</h2><p>Local device results and recovery evidence.</p></div></div><div class="card-body"><div class="timeline">${[["2026-08-13 14:08", "0.1.0-dev+8f2a", "Installed", "All health checks passed"], ["2026-08-11 09:44", "0.1.0-dev+7ca1", "Rolled back", "SHTC3 health check failed"], ["2026-08-08 16:20", "0.0.9", "Installed", "Initial paired build"]].map(([time, version, result, detail], index) => `<div class="timeline-row"><i class="${index === 1 ? "warning" : ""}"></i><time>${time}</time><div><strong>${version} · ${result}</strong><span>${detail}</span></div><button class="button small ghost">Details</button></div>`).join("")}</div></div></section>`;
}

function backupPage() {
  const steps = [["overview", "Overview"], ["export", "Export"], ["preview", "Import preview"], ["conflicts", "Resolve conflicts"], ["result", "Result"]];
  let content = "";
  if (state.backupStep === "export") {
    content = `<div class="wizard-layout"><article class="surface-card"><div class="card-header"><div><h2>Create encrypted backup</h2><p>age-encrypted, portable between macOS and Windows.</p></div>${status("LOCAL ONLY", "info")}</div><div class="card-body"><label class="field"><span>Backup password</span><input class="input" type="password" value="correct horse battery staple"><small>Use a password you can transfer separately; it cannot be recovered.</small></label><label class="field" style="margin-top:14px"><span>Confirm password</span><input class="input" type="password" value="correct horse battery staple"></label><label class="check-row" style="margin-top:18px"><input type="checkbox" checked><span><strong>Include user-entered Provider credentials</strong><small>Codex/Cursor-owned tokens remain excluded.</small></span></label><button class="button primary" style="margin-top:18px" data-action="export-backup">${icon("download")}Create .age backup</button></div></article><aside class="surface-card"><div class="card-header"><div><h2>Export manifest</h2><p>Preview before encryption.</p></div></div><div class="card-body"><dl class="definition-list">${definition("Provider configs", "5")}${definition("User secrets", "3 references")}${definition("Web settings", "Included")}${definition("Device metadata", "2 cached profiles")}${definition("History database", "Excluded")}${definition("Pairing tokens", "Excluded")}${definition("Web sessions", "Excluded")}</dl></div></aside></div>`;
  } else if (state.backupStep === "preview") {
    content = `<div class="wizard-layout"><article class="surface-card"><div class="card-header"><div><h2>Import backup</h2><p>Nothing changes until preview, conflict resolution and confirmation finish.</p></div></div><div class="card-body"><div class="drop-zone">${icon("upload", "icon-lg")}<strong>s3deck-studio-mac-2026-08-15.age</strong><span>4.2 KiB · schema 1 · encrypted</span><button class="button">Choose another file</button></div><label class="field" style="margin-top:18px"><span>Backup password</span><input class="input" type="password" value="correct horse battery staple"></label><button class="button primary" style="margin-top:18px" data-action="backup-step" data-step="conflicts">Decrypt and inspect</button></div></article><aside class="surface-card"><div class="card-header"><div><h2>Safe failure states</h2><p>No partial import is ever committed.</p></div></div><div class="card-body"><div class="stack"><div class="callout danger">${icon("warning")}<div><strong>Wrong password</strong><span>Authentication fails before any item is read.</span></div></div><div class="callout warning">${icon("warning")}<div><strong>Unsupported schema</strong><span>Keep the current configuration and suggest a compatible Companion.</span></div></div><div class="callout">${icon("shield")}<div><strong>Damaged archive</strong><span>Hash or manifest mismatch cancels the entire transaction.</span></div></div></div></div></aside></div>`;
  } else if (state.backupStep === "conflicts") {
    content = `<article class="surface-card"><div class="card-header"><div><h2>Review 3 conflicts</h2><p>Default action is merge; every replacement remains explicit.</p></div>${status("NO CHANGES YET", "warning")}</div><div class="card-body"><div class="conflict-list">${[["OpenRouter", "Endpoint and credential changed", "Keep current"], ["Provider order", "Backup puts DeepSeek before Cursor", "Use backup"], ["LAN management", "Backup enables LAN access", "Keep current"]].map(([title, detail, choice]) => `<div class="conflict-row"><div><strong>${title}</strong><span>${detail}</span></div><select class="select" aria-label="Resolve ${title}"><option>${choice}</option><option>${choice === "Use backup" ? "Keep current" : "Use backup"}</option></select></div>`).join("")}</div><div class="import-summary"><div><span>New items</span><strong>2</strong></div><div><span>Unchanged</span><strong>7</strong></div><div><span>Conflicts</span><strong>3</strong></div><div><span>Excluded</span><strong>4</strong></div></div><div class="button-row" style="margin-top:18px"><button class="button" data-action="backup-step" data-step="preview">Back</button><button class="button" data-action="confirm-action" data-result="Provider-only import prepared">Import Providers only</button><button class="button primary" data-action="backup-step" data-step="result">Confirm merge</button><button class="button danger">Replace everything…</button></div></div></article>`;
  } else if (state.backupStep === "result") {
    content = `<div class="surface-card result-state"><div class="result-icon">${icon("check", "icon-lg")}</div><div class="eyebrow">Transactional import complete</div><h2>12 items imported safely</h2><p>2 Provider configurations were added, 3 conflicts were resolved, and the previous configuration was preserved as an automatic rollback snapshot.</p><div class="import-summary"><div><span>Providers</span><strong>+2</strong></div><div><span>Settings</span><strong>7</strong></div><div><span>Skipped</span><strong>4</strong></div></div><div class="button-row"><button class="button primary" data-action="navigate" data-page="providers">Review Providers</button><button class="button" data-action="backup-step" data-step="overview">Done</button></div></div>`;
  } else {
    content = `<div class="grid-2"><article class="surface-card backup-choice"><div class="choice-icon">${icon("download", "icon-lg")}</div><h2>Export configuration</h2><p>Build a password-protected age archive containing user-entered Provider settings, secrets, Web settings and cached device metadata.</p><button class="button primary" data-action="backup-step" data-step="export">Create backup</button></article><article class="surface-card backup-choice"><div class="choice-icon">${icon("upload", "icon-lg")}</div><h2>Import configuration</h2><p>Decrypt locally, inspect a redacted preview, choose merge, replace or Providers-only, then resolve each conflict.</p><button class="button" data-action="backup-step" data-step="preview">Choose backup</button></article></div>`;
  }
  return `${pageHeading("Portable configuration", "Backup & Restore", "以事务方式导出或导入配置；历史、配对信任、Web 会话和串口缓冲永远不进入备份。", status("age encrypted", "success"))}<div class="wizard-steps">${steps.map(([id, label], index) => `<button class="wizard-step${state.backupStep === id ? " active" : ""}" data-action="backup-step" data-step="${id}"><span>${String(index + 1).padStart(2, "0")}</span>${label}</button>`).join("")}</div><section class="section-block">${content}</section>`;
}

function diagnosticsPage() {
  const tabs = [["health", "Health"], ["events", "Event log"], ["privacy", "Privacy report"], ["support", "Support bundle"]];
  let body = "";
  if (state.diagnosticsTab === "events") {
    const events = [["10:36:18.422", "INFO", "device_link", "Deck heartbeat accepted", "deck_A17F…92C1"], ["10:36:14.009", "WARN", "provider.cursor", "Snapshot marked stale", "AUTH_STALE"], ["10:35:58.771", "INFO", "serial", "Web observer connected", "client_…38F2"], ["10:35:42.114", "INFO", "provider.deepseek", "Snapshot normalized", "184 ms"], ["10:34:02.451", "ERROR", "provider.intranet", "Private endpoint unavailable", "ECONNREFUSED"]];
    body = `<div class="filter-row"><label class="field compact-field"><span>Level</span><select class="select"><option>All levels</option><option>Warnings & errors</option></select></label><label class="field compact-field"><span>Module</span><select class="select"><option>All modules</option><option>Providers</option><option>Device link</option><option>Serial</option></select></label><label class="field compact-field grow"><span>Search redacted logs</span><input class="input" type="search" placeholder="Code, module or event"></label><button class="button danger" data-action="clear-logs">Clear logs</button></div><div class="data-table-wrap section-block"><table class="data-table log-table"><thead><tr><th>Time</th><th>Level</th><th>Module</th><th>Event</th><th>Safe detail</th></tr></thead><tbody>${events.map(([time, level, module, event, detail]) => `<tr><td class="mono">${time}</td><td>${status(level, level === "ERROR" ? "danger" : level === "WARN" ? "warning" : "info")}</td><td><code>${module}</code></td><td>${event}</td><td class="mono">${detail}</td></tr>`).join("")}</tbody></table></div>`;
  } else if (state.diagnosticsTab === "privacy") {
    body = `<div class="grid-2"><article class="surface-card"><div class="card-header"><div><h2>Collection boundary</h2><p>Runtime data that may be retained locally.</p></div>${status("LOCAL", "success")}</div><div class="card-body"><div class="privacy-matrix">${[["Normalized Provider metrics", "Stored · 90 days", "success"], ["Anonymous session totals", "Stored · optional", "info"], ["Diagnostic events", "Stored · 7 days", "info"], ["Prompt / response content", "Never collected", "success"], ["Absolute paths", "Redacted", "success"], ["Serial body", "Current session buffer only", "warning"]].map(([label, value, tone]) => `<div><strong>${label}</strong>${status(value, tone)}</div>`).join("")}</div></div></article><article class="surface-card"><div class="card-header"><div><h2>Outbound connections</h2><p>No product telemetry endpoint exists.</p></div>${status("NO TELEMETRY", "info")}</div><div class="card-body"><dl class="definition-list">${definition("Provider APIs", "Only configured endpoints")}${definition("Update checks", "Configured release source")}${definition("Device Hub", "Pinned local WSS")}${definition("Analytics", "None")}${definition("Crash upload", "None")}</dl><button class="button" style="margin-top:18px">Export privacy report</button></div></article></div>`;
  } else if (state.diagnosticsTab === "support") {
    body = `<div class="wizard-layout"><article class="surface-card"><div class="card-header"><div><h2>Build support bundle</h2><p>Preview every category before export.</p></div></div><div class="card-body">${toggleRow("Companion manifest", "Version, platform and module status", true)}${toggleRow("Redacted event log", "Last 24 hours; secrets and paths removed", true)}${toggleRow("Deck memory ring", "Structured health only; no serial body", true)}${toggleRow("Configuration schema", "Keys only; no values or secret refs", true)}<button class="button primary" style="margin-top:18px" data-action="export-diagnostics">${icon("download")}Build diagnostics.zip</button></div></article><aside class="surface-card"><div class="card-header"><div><h2>Bundle manifest</h2><p>Estimated size 148 KiB.</p></div></div><div class="card-body"><pre class="code-panel">manifest.json\ncompanion-health.json\ndeck-health-A17F.json\nevents-redacted.jsonl\nschema-summary.json\nSHA256SUMS</pre><div class="callout" style="margin-top:16px">${icon("shield")}<div><strong>Review before sharing</strong><span>The app does not upload this archive automatically.</span></div></div></div></aside></div>`;
  } else {
    body = `<section class="metric-grid">${metricCard("Overall health", "NOMINAL", "1 isolated Provider warning")}${metricCard("Event loop", "2.4 ms", "p95 over last hour")}${metricCard("Device RTT", "18 ms", "Pinned WSS · stable")}${metricCard("Memory", "42.8 MiB", "No growth anomaly")}</section><section class="grid-2 section-block"><article class="surface-card"><div class="card-header"><div><h2>Subsystem health</h2><p>Failures stay isolated by boundary.</p></div></div><div class="card-body"><div class="check-list">${[["Management Web", "HEALTHY", "success"], ["Device Hub", "HEALTHY", "success"], ["SQLite history", "HEALTHY", "success"], ["Provider: Cursor", "AUTH_STALE", "warning"], ["Provider: Intranet LLM", "UNAVAILABLE", "danger"], ["Serial bridge", "DISARMED", "info"]].map(([label, value, tone]) => `<div><span>${icon(tone === "danger" ? "warning" : tone === "warning" ? "clock" : "check")}${label}</span>${status(value, tone)}</div>`).join("")}</div></div></article><article class="surface-card"><div class="card-header"><div><h2>Deck health</h2><p>S3-RLCD-A17F · heartbeat 4s ago</p></div>${status("ONLINE", "success")}</div><div class="card-body"><dl class="definition-list">${definition("Display", "OK · 400×300")}${definition("RTC", "OK · calibrated")}${definition("SHTC3", "OK · CRC valid")}${definition("NVS", "OK · active config v3")}${definition("UART", "Not installed · AI Page")}${definition("Recovery", "Available · BOOT hold")}</dl></div></article></section>`;
  }
  return `${pageHeading("Evidence without secrets", "Diagnostics", "定位 Companion、Provider 与 Deck 的问题，同时保持日志、导出和支持包的脱敏边界。", `<button class="button" data-action="refresh">${icon("refresh")}Run health check</button><button class="button primary" data-action="export-diagnostics">${icon("download")}Export bundle</button>`)}${panelTabs(tabs, state.diagnosticsTab, "diagnostics-tab")}<section class="section-block">${body}</section>`;
}

function setupPage() {
  const states = [["wifi", "Wi-Fi"], ["validating", "Validating"], ["pair", "Pairing"], ["profiles", "Profiles"], ["settings", "Settings"], ["success", "Success"], ["failed", "Failure"]];
  return `${pageHeading("Deck-owned recovery", "Setup / Recovery", "手机优先的临时页面，只在随机 WPA2 Setup AP 中开放；原型展示全部步骤、失败恢复和危险操作。", `${status("AP active · 08:42", "warning")}<button class="button" data-action="setup-step" data-step="wifi">Restart flow</button>`)}<section class="setup-stage"><div class="phone-frame" aria-label="Setup 手机页面预览"><div class="phone-screen">${setupPhone()}</div></div><div class="setup-notes"><article class="surface-card"><div class="card-header"><div><h2>Flow states</h2><p>点击状态切换手机原型</p></div></div><div class="card-body"><div class="filter-row">${states.map(([id, label]) => `<button class="chip${state.setupStep === id ? " active" : ""}" data-action="setup-step" data-step="${id}">${label}</button>`).join("")}</div></div></article><article class="surface-card"><div class="card-header"><div><h2>Recovery invariants</h2><p>实现时不可弱化的交互承诺</p></div></div><div class="card-body"><dl class="definition-list">${definition("Existing Wi-Fi", "Preserved until candidate validates")}${definition("Setup timeout", "10 minutes of inactivity")}${definition("Clear Wi-Fi", "Fresh token + second confirmation")}${definition("Pairing", "Explicit host:port + six-digit code")}${definition("Secrets", "Provider API keys never visible")}</dl></div></article><div class="callout warning">${icon("warning", "icon-lg")}<div><strong>Setup 页面资源非常有限</strong><span>正式实现应保持原生 HTML/CSS、无外部字体/脚本/CDN，并优先保证手机 375px 宽度下可用。</span></div></div></div></section>`;
}

function setupPhone() {
  const header = `<div class="phone-status"><span>10:36</span><span>Setup AP · ▮▮▮</span></div><div class="phone-header">${brandLockup()}</div>`;
  const steps = `<div class="phone-stepper"><span class="phone-step ${["wifi", "validating", "failed"].includes(state.setupStep) ? "active" : "done"}"></span><span class="phone-step ${["pair", "profiles"].includes(state.setupStep) ? "active" : ["settings", "success"].includes(state.setupStep) ? "done" : ""}"></span><span class="phone-step ${state.setupStep === "settings" ? "active" : state.setupStep === "success" ? "done" : ""}"></span></div>`;
  return `${header}<div class="phone-body">${steps}${setupPhoneBody()}</div>`;
}

function setupPhoneBody() {
  if (state.setupStep === "validating") return `<div class="eyebrow">Step 1 of 3</div><h2>正在验证网络</h2><p>旧配置会继续保留，直到 Studio-2G 同时完成关联与 DHCP。</p><div class="phone-card"><div class="callout"><div class="avatar">01</div><div><strong>Connecting to Studio-2G</strong><span>12 seconds elapsed · timeout in 8s</span></div></div><div class="progress" style="--progress:62%;margin-top:16px"><span></span></div></div><button class="phone-button secondary" data-action="setup-step" data-step="wifi">Cancel candidate</button>`;
  if (state.setupStep === "failed") return `<div class="eyebrow">Recovery available</div><h2>新网络无法连接</h2><p>认证失败。旧网络未被覆盖，Setup AP 将继续开放。</p><div class="phone-card"><div class="callout danger">${icon("warning")}<div><strong>AUTH_FAILED</strong><span>请确认密码，或选择其他 2.4 GHz 网络。</span></div></div><button class="phone-button" data-action="setup-step" data-step="wifi">修改并重试</button><button class="phone-button secondary" data-action="setup-step" data-step="pair">继续使用旧网络</button></div>`;
  if (state.setupStep === "pair") return `<div class="eyebrow">Step 2 of 3</div><h2>配对 Companion</h2><p>在电脑的 Companion 控制台生成一次性六位码，然后在这里完成证书固定。</p><div class="phone-card"><label class="field"><span>Hub host:port</span><input class="input" value="192.168.4.2:7443"></label><label class="field" style="margin-top:12px"><span>Six-digit code</span><input class="input mono" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" value="481209"></label><button class="phone-button" data-action="setup-step" data-step="profiles">Pair Companion</button><button class="phone-button secondary" data-action="setup-step" data-step="settings">Skip for now</button></div>`;
  if (state.setupStep === "profiles") return `<div class="eyebrow">Companion profiles</div><h2>已配对电脑</h2><p>Deck 最多保存五个 Profile，同时只有一个 Active Companion。</p><div class="phone-card"><div class="identity-row"><div class="avatar">SM</div><div style="flex:1"><strong>Studio Mac</strong><span>192.168.1.20:7443</span></div>${status("ACTIVE", "success")}</div><button class="phone-button secondary">View fingerprint</button></div><div class="phone-card"><div class="identity-row"><div class="avatar">LW</div><div style="flex:1"><strong>Lab Windows</strong><span>Last connected 8 days ago</span></div></div><div class="button-row"><button class="phone-button secondary" style="width:auto;flex:1">Make active</button><button class="phone-button danger" style="width:auto;flex:1" data-action="setup-revoke">Revoke</button></div></div><button class="phone-button" data-action="setup-step" data-step="settings">Continue</button>`;
  if (state.setupStep === "settings") return `<div class="eyebrow">Step 3 of 3</div><h2>设备设置</h2><p>温度偏移与 Wi-Fi 配置独立保存；清除 Wi-Fi 不会删除校准和 Companion Profiles。</p><div class="phone-card"><label class="field"><span>Temperature offset (°C)</span><input class="input" type="number" min="-15" max="15" step="0.1" value="-4.0"><small>Deck preview: raw 28.8°C → calibrated 24.8°C</small></label><button class="phone-button" data-action="setup-step" data-step="success">Save offset</button></div><div class="phone-card"><strong class="tone-danger">Wi-Fi recovery</strong><p class="helper">需要新鲜确认 Token 和第二次明确确认。</p><button class="phone-button danger" data-action="clear-wifi">Clear saved Wi-Fi…</button></div>`;
  if (state.setupStep === "success") return `<div class="eyebrow">Setup complete</div><div class="empty-state" style="min-height:470px;padding:10px"><div class="empty-icon">${icon("check", "icon-lg")}</div><h2>Deck 已准备就绪</h2><p>Studio-2G 已提交，Studio Mac 已成为 Active Companion，温度偏移保存为 −4.0°C。</p>${status("Closing AP in 08s", "success")}<button class="phone-button secondary" data-action="setup-step" data-step="profiles">Manage profiles</button></div>`;
  const networks = [["Studio-2G", "−52 dBm · WPA2"], ["Lab-IoT", "−68 dBm · WPA2"], ["Guest", "−74 dBm · Open"]];
  return `<div class="eyebrow">Step 1 of 3</div><h2>连接 Wi‑Fi</h2><p>选择一个 2.4 GHz 网络。验证成功前，最后一个可用配置不会被替换。</p><button class="phone-button secondary" data-action="scan-wifi">${icon("refresh", "icon-sm")} Scan networks</button><div class="phone-card">${networks.map(([name, info]) => `<button class="network-option" data-action="select-network" data-network="${name}"><div><strong>${name}</strong><span>${info}</span></div>${state.setupNetwork === name ? icon("check") : icon("network")}</button>`).join("")}</div><div class="phone-card"><label class="field"><span>Password for ${state.setupNetwork}</span><input class="input" type="password" value="safe-password"><small>Stored only after connection validation succeeds.</small></label><button class="phone-button" data-action="setup-step" data-step="validating">Validate and activate</button></div>`;
}

const deckScreens = [
  ["setup", "First setup", "P0"],
  ["pair", "Pair Companion", "P0"],
  ["codex", "Codex AI Page", "M2"],
  ["provider", "Provider Page", "M3"],
  ["config", "No Provider hint", "M3"],
  ["stale", "STALE snapshot", "M2"],
  ["reconnecting", "Companion failover", "M2"],
  ["offline", "Agent offline", "M2"],
  ["serial", "Serial session", "M4"],
  ["serial-stats", "Serial stats subview", "M4"],
  ["degraded", "Degraded operation", "P0"],
  ["ota", "OTA progress", "M5"],
  ["rollback", "Rollback result", "M5"],
];

function deckPage() {
  return `${pageHeading("400 × 300 monochrome", "Deck RLCD Screens", "所有状态依靠文字、边框、填充和纹理表达，不依赖颜色；缺失字段隐藏，未知数值绝不显示为 0。", `${status("TX DISARMED", "success")}<button class="button" data-action="deck-next">KEY · Next page</button>`)}<div class="deck-toolbar callout">${icon("chip", "icon-lg")}<div><strong>Physical screen simulator</strong><span>比例与正式 400×300 横屏一致。浏览器抗锯齿仅用于评审，正式字形仍需实板验证。</span></div></div><section class="deck-stage"><div><div class="deck-device"><div class="deck-screen">${renderDeckScreen()}</div><div class="deck-controls"><button class="hardware-button" data-action="deck-key">KEY</button><button class="hardware-button" data-action="deck-boot">BOOT</button></div></div></div><aside class="deck-spec surface-card"><div class="card-header"><div><h2>Screen inventory</h2><p>选择一个状态进行评审</p></div></div><div class="card-body"><div class="screen-list">${deckScreens.map(([id, label, milestone]) => `<button class="screen-option${state.deckScreen === id ? " active" : ""}" data-action="deck-screen" data-screen="${id}"><span>${label}</span><code>${milestone}</code></button>`).join("")}</div></div><div class="card-footer"><span class="helper">KEY cycles AI Pages</span><span class="helper">BOOT exits Serial</span></div></aside></section>`;
}

function deckStatusBar(companion = "AGENT ●") {
  return `<div class="rlcd-statusbar"><span class="rlcd-status-item">10:36</span><span class="rlcd-status-item">24.8°C</span><span class="rlcd-status-item">WI-FI ▮▮▮</span><span class="rlcd-status-item">${companion}</span></div>`;
}

function renderDeckScreen() {
  const screen = state.deckScreen;
  if (screen === "setup") return `<div class="rlcd-screen">${deckStatusBar("SETUP AP")}<div class="rlcd-center"><h2>FIRST SETUP</h2><p>Connect phone or computer to</p><div class="rlcd-code" style="letter-spacing:.02em">S3-RLCD-A17F</div><p>PASS · MINT-WAVE-7294</p><p>HTTP · 192.168.4.1</p></div><div class="rlcd-footer"><span>AP CLOSES · 09:42</span><span>BOOT · RESTART</span></div></div>`;
  if (screen === "pair") return `<div class="rlcd-screen">${deckStatusBar("NO AGENT")}<div class="rlcd-center"><h2>PAIR COMPANION</h2><p>Wi-Fi connected · Studio-2G</p><div class="rlcd-code" style="font-size:clamp(10px,2.2vw,24px);letter-spacing:0">192.168.4.1</div><p>Open Setup / Recovery</p><p>Enter Hub address + 6-digit code</p></div><div class="rlcd-footer"><span>TX · DISARMED</span><span>BOOT HOLD · SETUP</span></div></div>`;
  if (screen === "provider") return `<div class="rlcd-screen">${deckStatusBar()}<div class="rlcd-body"><div class="rlcd-title-row"><div class="rlcd-title">CURSOR</div><span class="rlcd-badge striped">EXPERIMENTAL</span></div><div class="rlcd-quota"><span>Balance</span><strong style="font-size:clamp(12px,2.4vw,26px)">$18.42</strong><span></span><span></span></div><div class="rlcd-quota"><span>Usage</span><div class="rlcd-progress" style="--progress:71%"><span></span></div><strong>71%</strong><span></span></div><div class="rlcd-quota"><span>Reset</span><strong style="grid-column:2/5">2026-08-16</strong></div><div class="rlcd-quota"><span>Updated</span><strong style="grid-column:2/5">10:35:42</strong></div><div class="rlcd-footer"><span>WEB ON COMPUTER</span><span>KEY · NEXT</span></div></div></div>`;
  if (screen === "config") return `<div class="rlcd-screen">${deckStatusBar()}<div class="rlcd-center"><h2>ADD AI PROVIDER</h2><p>No additional Provider pages are enabled</p><div class="rlcd-code" style="font-size:clamp(10px,2.2vw,22px);letter-spacing:0">WEB ON COMPUTER</div><p>127.0.0.1:7777</p><p>Configure order in Companion</p></div><div class="rlcd-footer"><span>TX · DISARMED</span><span>KEY · CODEX</span></div></div>`;
  if (screen === "stale") return `<div class="rlcd-screen">${deckStatusBar("AGENT OFFLINE")}<div class="rlcd-body"><div class="rlcd-title-row"><div class="rlcd-title">CODEX</div><span class="rlcd-badge striped">STALE · 47m</span></div><div class="rlcd-quota"><span>Primary</span><div class="rlcd-progress" style="--progress:62%"><span></span></div><strong>62%</strong><span>02:44</span></div><div class="rlcd-quota"><span>Weekly</span><div class="rlcd-progress" style="--progress:78%"><span></span></div><strong>78%</strong><span>Tue</span></div><div class="rlcd-separator"></div><div class="rlcd-session"><strong>LAST VERIFIED SNAPSHOT</strong><span>09:49</span><span>47m</span></div><div class="rlcd-meta"><span>Values hidden after 24h</span><span>TX DISARMED</span></div><div class="rlcd-footer"><span>RECOVERY · 192.168.1.88</span><span>KEY · NEXT</span></div></div></div>`;
  if (screen === "reconnecting") return `<div class="rlcd-screen">${deckStatusBar("AGENT …")}<div class="rlcd-body"><div class="rlcd-title-row"><div class="rlcd-title">COMPANION FAILOVER</div><span class="rlcd-badge striped">18s</span></div><div class="rlcd-quota"><span>Active</span><strong style="grid-column:2/4">STUDIO MAC</strong><span>OFFLINE</span></div><div class="rlcd-quota"><span>Next</span><strong style="grid-column:2/4">LAB WINDOWS</strong><span>P2</span></div><div class="rlcd-quota"><span>Retry</span><div class="rlcd-progress" style="--progress:60%"><span></span></div><strong>12s</strong><span>30s</span></div><div class="rlcd-meta"><span>Last snapshot preserved</span><span>TX DISARMED</span></div><div class="rlcd-footer"><span>NO PROFILE PREEMPTION</span><span>BOOT HOLD · SETUP</span></div></div></div>`;
  if (screen === "offline") return `<div class="rlcd-screen">${deckStatusBar("AGENT OFFLINE")}<div class="rlcd-center"><h2>AGENT OFFLINE</h2><p>Last verified snapshot · 27h ago</p><div class="rlcd-code" style="font-size:clamp(11px,2.2vw,24px);letter-spacing:0">QUOTA HIDDEN</div><p>Reconnect Active Companion</p><p>Recovery · 192.168.1.88</p></div><div class="rlcd-footer"><span>TX · DISARMED</span><span>BOOT HOLD · SETUP</span></div></div>`;
  if (screen === "serial") return `<div class="rlcd-screen"><div class="rlcd-statusbar" style="grid-template-columns:1.2fr 1fr 1fr"><span>SERIAL</span><span>USB TX</span><span style="text-align:right">00:18:32</span></div><div class="rlcd-body"><div class="rlcd-title-row"><div class="rlcd-title">115200 · 8N1</div><span class="rlcd-badge filled">ARMED</span></div><div class="rlcd-serial-grid"><div class="rlcd-stat"><small>RX TOTAL</small><strong>12.48 MB</strong></div><div class="rlcd-stat"><small>RX RATE</small><strong>11.3 KiB/s</strong></div><div class="rlcd-stat"><small>TX TOTAL</small><strong>18.42 KB</strong></div><div class="rlcd-stat"><small>TX RATE</small><strong>0.2 KiB/s</strong></div></div><div class="rlcd-meta"><span>UART ERR 0</span><span>OVERWRITE 0</span><span>WEB 1</span></div><div class="rlcd-footer"><span>KEY · STATS</span><span>BOOT · EXIT</span><span>REFRESH · 1s</span></div></div></div>`;
  if (screen === "serial-stats") return `<div class="rlcd-screen"><div class="rlcd-statusbar" style="grid-template-columns:1.2fr 1fr 1fr"><span>SERIAL STATS</span><span>WEB TX</span><span style="text-align:right">09:42</span></div><div class="rlcd-body"><div class="rlcd-title-row"><div class="rlcd-title">FLOW HEALTH</div><span class="rlcd-badge filled">ARMED</span></div><div class="rlcd-serial-grid"><div class="rlcd-stat"><small>UART OVERFLOW</small><strong>0</strong></div><div class="rlcd-stat"><small>UART ERRORS</small><strong>0</strong></div><div class="rlcd-stat"><small>ESP OVERWRITE</small><strong>0 B</strong></div><div class="rlcd-stat"><small>AGENT OVERWRITE</small><strong>1.02 KB</strong></div></div><div class="rlcd-meta"><span>USB REJECT 28 B</span><span>WEB CLIENTS 2</span></div><div class="rlcd-footer"><span>KEY · TOTALS</span><span>BOOT · EXIT</span><span>REFRESH · 1s</span></div></div></div>`;
  if (screen === "degraded") return `<div class="rlcd-screen">${deckStatusBar("AGENT ●")}<div class="rlcd-body"><div class="rlcd-title-row"><div class="rlcd-title">DEGRADED OPERATION</div><span class="rlcd-badge striped">2 ISSUES</span></div><div class="rlcd-quota"><span>RTC</span><strong style="grid-column:2/4">UNAVAILABLE</strong><span>RETRY</span></div><div class="rlcd-quota"><span>SHTC3</span><strong style="grid-column:2/4">CRC ERROR</strong><span>#12</span></div><div class="rlcd-quota"><span>Wi-Fi</span><strong style="grid-column:2/4">CONNECTED</strong><span>OK</span></div><div class="rlcd-quota"><span>AI</span><strong style="grid-column:2/4">LAST SNAPSHOT</strong><span>8m</span></div><div class="rlcd-footer"><span>HEALTHY SERVICES CONTINUE</span><span>BOOT HOLD · SETUP</span></div></div></div>`;
  if (screen === "ota") return `<div class="rlcd-screen">${deckStatusBar("AGENT ●")}<div class="rlcd-center"><h2>FIRMWARE UPDATE</h2><p>0.1.0 → 0.2.0 · SIGNED</p><div class="rlcd-progress" style="--progress:68%;width:78%;height:12%;margin:5% auto"><span></span></div><p>WRITING OTA_1 · 68%</p><p>Do not disconnect power</p></div><div class="rlcd-footer"><span>OLD FIRMWARE PRESERVED</span><span>TX · DISARMED</span></div></div>`;
  if (screen === "rollback") return `<div class="rlcd-screen">${deckStatusBar("AGENT ●")}<div class="rlcd-center"><h2>UPDATE ROLLED BACK</h2><div class="rlcd-code" style="font-size:clamp(11px,2.3vw,24px);letter-spacing:0">HEALTH CHECK FAILED</div><p>Restored 0.1.0 from ota_0</p><p>Display + recovery remain available</p></div><div class="rlcd-footer"><span>VIEW DETAILS ON COMPUTER</span><span>BOOT · RECOVERY</span></div></div>`;
  return `<div class="rlcd-screen">${deckStatusBar()}<div class="rlcd-body"><div class="rlcd-title-row"><div class="rlcd-title">CODEX</div><span class="rlcd-badge">VERIFIED</span></div><div class="rlcd-quota"><span>Primary</span><div class="rlcd-progress" style="--progress:62%"><span></span></div><strong>62%</strong><span>02:44</span></div><div class="rlcd-quota"><span>Weekly</span><div class="rlcd-progress" style="--progress:78%"><span></span></div><strong>78%</strong><span>Tue</span></div><div class="rlcd-separator"></div><div class="rlcd-session"><strong>s3-rlcd-deck</strong><span>RUNNING?</span><span>12m</span></div><div class="rlcd-meta"><span>146k tokens</span><span>Context 41%</span><span>+2 sessions</span></div><div class="rlcd-footer"><span>TX · DISARMED</span><span>KEY · NEXT</span><span>HOLD · SERIAL</span></div></div></div>`;
}

function trayPage() {
  const trayCopy = {
    running: ["v0.1.0 · Running", "1 Deck connected", "Stop Companion"],
    stopped: ["v0.1.0 · Stopped", "Listeners are offline", "Start Companion"],
    starting: ["v0.1.0 · Starting", "Opening local listeners…", "Stop Companion"],
    error: ["v0.1.0 · Error", "Port 7777 is already in use", "Start Companion"],
  }[state.trayState];
  return `${pageHeading("Desktop shell", "Tray / Menu States", "macOS 菜单栏和 Windows 托盘只提供快速状态、打开控制台、启动/停止和退出；复杂操作回到 Web。", "")}<div class="filter-row" style="margin-bottom:18px">${["running", "starting", "stopped", "error"].map((item) => `<button class="chip${state.trayState === item ? " active" : ""}" data-action="tray-state" data-state="${item}">${item.toUpperCase()}</button>`).join("")}</div><section class="tray-stage"><div class="desktop-preview"><div class="desktop-menubar"><span>S3 RLCD Deck</span><span>10:36 · 24.8°C · ◈</span></div><div class="desktop-wallpaper"><div class="tray-menu"><div class="tray-status"><strong>${trayCopy[0]}</strong>${trayCopy[1]}</div><div class="tray-separator"></div><button class="tray-item" data-action="open-console"><span>Open Console</span><span>⌘O</span></button><button class="tray-item" data-action="tray-toggle"><span>${trayCopy[2]}</span></button><div class="tray-separator"></div><button class="tray-item"><span>About S3 RLCD Deck</span></button><button class="tray-item" data-action="quit-companion"><span>Quit</span><span>⌘Q</span></button></div></div></div><div class="stack"><article class="surface-card"><div class="card-header"><div><h2>State copy</h2><p>动态状态使用简短、可执行的文案</p></div>${status(state.trayState.toUpperCase(), state.trayState === "running" ? "success" : state.trayState === "error" ? "danger" : "warning")}</div><div class="card-body"><dl class="definition-list">${definition("Tooltip", `S3 RLCD Deck Companion · ${trayCopy[0]} · ${trayCopy[1]}`)}${definition("Primary action", "Open Console")}${definition("Runtime action", trayCopy[2])}${definition("Error recovery", "Show exact local cause, keep Start available")}</dl></div></article><div class="callout">${icon("shield")}<div><strong>Open Console uses a one-time desktop grant</strong><span>It creates a local 30-second single-use grant, exchanges it for an HttpOnly session, then removes the token from the visible URL.</span></div></div></div></section>`;
}

function loginPage() {
  let accessBody = "";
  if (state.accessStep === "grant") accessBody = `<div class="access-grant"><div class="grant-ring"><span>18s</span></div><h2>Opening local console</h2><p>A one-time desktop grant from the tray is being exchanged for an HttpOnly session. The grant is removed from the visible URL immediately.</p><button class="button primary" type="button" data-action="login">Continue now</button></div>`;
  else if (state.accessStep === "manual") accessBody = `<div class="eyebrow">Manual recovery</div><h2>Use management token</h2><p>Use this only when tray access is unavailable. The token remains in the operating system credential vault.</p><label class="field"><span>Management token</span><input class="input mono" type="password" value="local-development-token"><small>Paste the local management token, never a device or Provider token.</small></label><button class="button primary" style="margin-top:18px" type="button" data-action="login">Unlock local console</button>`;
  else if (state.accessStep === "locked") accessBody = `<div class="eyebrow tone-danger">Rate limited</div><h2>Try again in 00:42</h2><p>Several invalid management tokens were rejected from this browser. Device connections and Provider collection continue normally.</p><div class="callout danger">${icon("warning")}<div><strong>LOCAL_AUTH_RATE_LIMITED</strong><span>Close this tab or wait for the local cooldown. Tokens are never logged.</span></div></div><button class="button" style="margin-top:18px" data-action="access-step" data-step="manual">Return to token entry</button>`;
  else accessBody = `<div class="eyebrow">Local management</div><h2>Unlock Companion</h2><p>Normally, choose Open Console from the tray for automatic one-time access.</p><div class="callout warning" style="margin-bottom:18px">${icon("clock")}<div><strong>Previous session expired</strong><span>Management sessions end after eight hours. No device connection was revoked.</span></div></div><div class="button-row"><button class="button primary" type="button" data-action="access-step" data-step="grant">Open from tray grant</button><button class="button" type="button" data-action="access-step" data-step="manual">Use token</button></div>`;
  return `<div class="login-review"><div class="filter-row">${[["grant", "Tray grant"], ["manual", "Manual token"], ["expired", "Expired"], ["locked", "Rate limited"]].map(([id, label]) => `<button class="chip${state.accessStep === id ? " active" : ""}" data-action="access-step" data-step="${id}">${label}</button>`).join("")}</div><div class="login-stage"><div class="login-panel"><div class="login-story">${brandLockup()}<h1>Your local AI usage instrument.</h1><p>Provider credentials stay on this computer. Deck receives only normalized, display-safe snapshots.</p><div class="login-boundary"><span>127.0.0.1:7777</span><span>NO EXTERNAL TELEMETRY</span></div></div><form class="login-form">${accessBody}<div class="callout" style="margin-top:18px">${icon("shield")}<div><strong>Loopback only by default</strong><span>Management and device tokens use separate authority.</span></div></div></form></div></div></div>`;
}

function emptyState(iconName, title, message) {
  return `<div class="surface-card empty-state"><div class="empty-icon">${icon(iconName, "icon-lg")}</div><h2>${title}</h2><p>${message}</p></div>`;
}

function renderExceptionalState() {
  const page = currentPage();
  if (state.viewState === "loading") {
    return `${pageHeading("Loading state", page.label, "保持页面结构稳定，并明确告知正在读取本机数据。", status("LOADING", "info"))}<div class="skeleton-grid" aria-label="正在加载"><div class="skeleton-card"><i></i><b></b><span></span></div><div class="skeleton-card"><i></i><b></b><span></span></div><div class="skeleton-card wide"><i></i><b></b><span></span><span></span><span></span></div></div>`;
  }
  if (state.viewState === "empty") {
    return `${pageHeading("Empty state", page.label, "没有可展示的记录时提供原因、边界和下一步，而不是留下空白画布。", "")}<div class="surface-card empty-state"><div class="empty-icon">${icon(page.icon, "icon-lg")}</div><h2>暂无 ${translateUICopy(page.label)} 数据</h2><p>该界面当前没有本机数据。添加配置、连接设备或扩大筛选范围后会显示在这里。</p><button class="button primary" data-action="view-default">Return to populated example</button></div>`;
  }
  return `${pageHeading("Recoverable error", page.label, "错误会说明影响范围，保留其他可用模块，并提供明确恢复动作。", status("PARTIAL FAILURE", "danger"))}<div class="surface-card error-state" role="alert"><div class="error-symbol">${icon("warning", "icon-lg")}</div><div><div class="eyebrow">LOCAL_READ_FAILED</div><h2>Could not load this surface</h2><p>The last valid data remains unchanged. Companion, Device Hub and Serial ownership were not restarted.</p><div class="button-row"><button class="button primary" data-action="view-default">Try again</button><button class="button" data-action="navigate" data-page="diagnostics">Open Diagnostics</button></div></div><code>request_id · local-019A…7F20</code></div>`;
}

function renderPage() {
  if (!state.loggedIn) return loginPage();
  if (state.viewState !== "default" && state.page !== "login") return renderExceptionalState();
  const pages = { login: loginPage, overview: overviewPage, providers: providersPage, "provider-editor": providerEditorPage, history: historyPage, sessions: sessionsPage, serial: serialPage, "serial-presets": serialPresetsPage, devices: devicesPage, network: networkPage, system: systemPage, updates: updatesPage, backup: backupPage, diagnostics: diagnosticsPage, setup: setupPage, deck: deckPage, tray: trayPage };
  return (pages[state.page] || overviewPage)();
}

function renderModal() {
  if (!state.modal) return "";
  const modal = modalContent(state.modal);
  return `<div class="modal-backdrop" data-action="close-modal-backdrop"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="modal-title"><div class="modal-header"><div><div class="eyebrow">Prototype action</div><h2 id="modal-title">${modal.title}</h2></div><button class="icon-button" data-action="close-modal" aria-label="关闭对话框">${icon("x")}</button></div><div class="modal-body">${modal.body}</div><div class="modal-footer">${modal.footer}</div></section></div>`;
}

function modalContent(type) {
  if (type === "pair-code") return { title: "Pair a new Deck", body: `<p>先把运行 Companion 的电脑连接到 Deck 的随机 WPA2 Setup AP，然后把下面的地址和一次性代码输入 Setup 页面。</p><div class="grid-2"><div class="surface-card card-body"><div class="metric-label">Device Hub</div><div class="code-value" style="margin-top:8px">192.168.4.2:7443</div></div><div class="surface-card card-body"><div class="metric-label">Expires in</div><div class="code-value" style="margin-top:8px">04:52</div></div></div><div class="rlcd-code" style="text-align:center;color:var(--text);border-color:var(--text)">481 209</div><div class="callout warning">${icon("warning")}<div><strong>Single use</strong><span>Do not paste this code into chat, logs or issue evidence.</span></div></div>`, footer: `<button class="button" data-action="copy-code">${icon("copy")}Copy address + code</button><button class="button primary" data-action="close-modal">Done</button>` };
  if (type === "revoke-device" || type === "setup-revoke") return { title: "Revoke device trust?", body: `<div class="callout danger">${icon("warning", "icon-lg")}<div><strong>This Deck will disconnect immediately</strong><span>Reconnecting requires a fresh Setup AP pairing flow. Wi-Fi and device settings are preserved.</span></div></div><p style="margin-top:16px">输入 <strong class="mono">REVOKE</strong> 进行确认。</p><label class="field"><span>Confirmation</span><input class="input mono" value="REVOKE"></label>`, footer: `<button class="button" data-action="close-modal">Cancel</button><button class="button danger" data-action="confirm-destructive" data-result="Device trust revoked">Revoke trust</button>` };
  if (type === "clear-wifi") return { title: "清除已保存的 Wi‑Fi？", body: `<div class="callout danger">${icon("warning", "icon-lg")}<div><strong>Deck will remain in Setup Mode</strong><span>Only Wi-Fi candidate, marker and slots are removed. Calibration and Companion Profiles remain.</span></div></div><p style="margin-top:16px">此确认 Token 将在 <strong>00:58</strong> 后过期。</p>`, footer: `<button class="button" data-action="close-modal">Keep Wi-Fi</button><button class="button danger" data-action="confirm-destructive" data-result="Wi-Fi cleared; Setup remains active">Clear Wi-Fi</button>` };
  if (type === "rotate-token") return { title: "Rotate Deck token", body: `<p>A device-bound six-digit rotation code will be issued. The current token stays valid until the Deck redeems the code.</p><div class="callout">${icon("shield")}<div><strong>No plaintext token is displayed here</strong><span>The new device token appears only once in the authenticated Deck redeem response.</span></div></div>`, footer: `<button class="button" data-action="close-modal">Cancel</button><button class="button primary" data-action="confirm-action" data-result="Rotation code issued">Issue rotation code</button>` };
  if (type === "ota-update") return { title: "Install signed firmware", body: `<div class="surface-card card-body"><dl class="definition-list">${definition("File", "s3-rlcd-deck-0.2.0.bin")}${definition("Board", "ESP32-S3-RLCD-4.2 · match")}${definition("Signature", "Ed25519 · valid")}${definition("Image hash", "SHA-256 · match")}${definition("Rollback", "0.1.0 remains in ota_0")}</dl></div><div class="callout warning" style="margin-top:16px">${icon("warning")}<div><strong>Deck may reboot more than once</strong><span>The new slot is valid only after display, peripherals, Wi-Fi and Companion health checks pass.</span></div></div>`, footer: `<button class="button" data-action="close-modal">Cancel</button><button class="button primary" data-action="start-ota">Install 0.2.0</button>` };
  if (type === "add-provider") return { title: "Add structured HTTP Provider", body: `<form class="form-grid"><label class="field"><span>Display name</span><input class="input" value="OpenRouter"></label><label class="field"><span>Template</span><select class="select"><option>Custom HTTPS JSON</option><option>AIHubMix</option><option>DeepSeek</option></select></label><label class="field full"><span>Endpoint</span><input class="input mono" type="url" value="https://openrouter.ai/api/v1/credits"></label><label class="field"><span>Polling</span><select class="select"><option>Every 5 minutes</option></select></label><label class="field"><span>Secret reference</span><input class="input" value="keychain://s3deck/openrouter"></label><label class="field full"><span>JSONPath mapping</span><textarea class="textarea mono">balance: $.data.total_credits&#10;usage: $.data.total_usage</textarea></label></form>`, footer: `<button class="button" data-action="close-modal">Cancel</button><button class="button" data-action="confirm-action" data-result="Test request succeeded">Test request</button><button class="button primary" data-action="confirm-action" data-result="Provider saved">Save Provider</button>` };
  if (type === "clear-history") return { title: "Clear all usage history?", body: `<div class="callout danger">${icon("warning", "icon-lg")}<div><strong>3,148 normalized snapshots will be deleted</strong><span>Provider configurations, credentials, device trust and serial presets are not affected.</span></div></div><p style="margin-top:16px">输入 <strong class="mono">CLEAR HISTORY</strong> 进行确认。此操作无法撤销。</p><label class="field"><span>Confirmation</span><input class="input mono" value="CLEAR HISTORY"></label>`, footer: `<button class="button" data-action="close-modal">Keep history</button><button class="button danger" data-action="confirm-destructive" data-result="Usage history cleared">Clear 3,148 snapshots</button>` };
  if (type === "stop-companion") return { title: "Stop Companion?", body: `<p>Management Web and Device Hub listeners will close. Connected Decks enter offline handling and retain their last valid snapshots.</p>`, footer: `<button class="button" data-action="close-modal">Cancel</button><button class="button danger" data-action="confirm-destructive" data-result="Companion stopped">Stop Companion</button>` };
  return { title: "Prototype action", body: `<p>This interaction is represented with in-memory mock state only.</p>`, footer: `<button class="button primary" data-action="close-modal">Done</button>` };
}

function renderToast() {
  if (!state.toast) return "";
  return `<div class="toast-region" role="status"><div class="toast">${icon(state.toast.tone === "danger" ? "warning" : "check", "icon-lg")}<div><strong>${state.toast.title}</strong><span>${state.toast.message}</span></div></div></div>`;
}

function renderStateInspector() {
  if (!state.stateInspector) return "";
  const viewStateLabels = { default: "默认", loading: "加载中", empty: "空状态", error: "错误" };
  const serialModeLabels = { text: "文本", hex: "HEX", mixed: "混合" };
  const setupStepLabels = { wifi: "选择 Wi-Fi", validating: "验证网络", pair: "配对 Companion", profiles: "Companion 配置档案", settings: "设备设置", success: "完成", failed: "失败" };
  const providerFilterLabels = { all: "全部", verified: "已验证", stale: "已过期", unavailable: "不可用" };
  const sessionFilterLabels = { all: "全部", verified: "已验证", inferred: "推断" };
  const trayStateLabels = { running: "运行中", starting: "正在启动", stopped: "已停止", error: "错误" };
  const providerEditorTabLabels = { request: "请求", mapping: "数据映射", preview: "测试预览", security: "安全" };
  const backupStepLabels = { overview: "概览", export: "导出", preview: "导入预览", conflicts: "解决冲突", result: "结果" };
  const diagnosticsTabLabels = { health: "健康", events: "事件日志", privacy: "隐私报告", support: "支持包" };
  const visibleState = {
    "设计方案": "C · 仪表面板",
    "当前页面": translateUICopy(currentPage().label),
    "当前模块": translateUICopy(currentGroup().label),
    "评审状态": viewStateLabels[state.viewState],
    "是否登录": state.loggedIn ? "是" : "否",
    "串口模式": serialModeLabels[state.serialMode],
    "Web TX": state.webTx ? "已取得" : "未取得",
    "串口已暂停": state.serialPaused ? "是" : "否",
    "Setup 步骤": setupStepLabels[state.setupStep],
    "Setup 网络": state.setupNetwork,
    "Deck 屏幕": translateUICopy(deckScreens.find(([id]) => id === state.deckScreen)?.[1] || state.deckScreen),
    "Provider 筛选": providerFilterLabels[state.providerFilter],
    "会话筛选": sessionFilterLabels[state.sessionFilter],
    "历史范围": state.historyRange.replace("d", " 天").replace("h", " 小时"),
    "托盘状态": trayStateLabels[state.trayState],
    "LAN 管理访问": state.lanManagement ? "已启用" : "已停用",
    "OTA 进度": `${state.otaProgress}%`,
    "Provider 编辑标签": providerEditorTabLabels[state.providerEditorTab],
    "已选设备": deckInventory[state.selectedDevice]?.name || state.selectedDevice,
    "备份步骤": backupStepLabels[state.backupStep],
    "诊断标签": diagnosticsTabLabels[state.diagnosticsTab],
  };
  return `<aside class="state-inspector" aria-label="原型状态"><div class="state-inspector-header"><strong>原型状态</strong><button class="icon-button" data-action="toggle-state" aria-label="关闭状态面板">${icon("x")}</button></div><pre>${escapeHTML(JSON.stringify(visibleState, null, 2))}</pre></aside>`;
}

function render() {
  const content = renderPage();
  const layout = renderVariantC(content);
  app.innerHTML = `<div class="prototype-root variant-c${state.mobileNav ? " mobile-nav-open" : ""}"><div class="prototype-banner">PROTOTYPE ONLY · C / INSTRUMENT PANEL · 全部数据为脱敏模拟值</div>${layout}${state.mobileNav ? `<button class="mobile-backdrop" data-action="mobile-nav" aria-label="关闭导航"></button>` : ""}${renderModal()}${renderToast()}${renderStateInspector()}</div>`;
  localizeVisibleUI(app);
  document.title = `${translateUICopy(currentPage().label)} · S3 RLCD Deck 仪表界面`;
}

function updateURL() {
  const url = new URL(location.href);
  url.searchParams.delete("variant");
  url.hash = state.page;
  history.replaceState(null, "", url);
}

function toast(title, message = "Mock state updated successfully.", tone = "success") {
  state.toast = { title, message, tone };
  render();
  window.setTimeout(() => {
    if (state.toast?.title === title) {
      state.toast = null;
      render();
    }
  }, 2600);
}

function navigate(page) {
  if (!allPages.some((item) => item.id === page)) return;
  state.page = page;
  state.viewState = "default";
  state.mobileNav = false;
  updateURL();
  render();
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  window.scrollTo({ top: 0, behavior: reduced ? "auto" : "smooth" });
}

function showModal(type) {
  state.modal = type;
  render();
  requestAnimationFrame(() => document.querySelector(".modal .icon-button")?.focus());
}

function closeModal() {
  state.modal = null;
  render();
}

function advanceDeck() {
  const ids = deckScreens.map(([id]) => id);
  state.deckScreen = ids[(ids.indexOf(state.deckScreen) + 1) % ids.length];
  render();
}

function startProviderTest(provider) {
  state.providerTest = provider;
  render();
  window.setTimeout(() => {
    state.providerTest = null;
    toast(`${provider} test succeeded`, "HTTP 200 · 184 ms · schema accepted.");
  }, 900);
}

function startOTA() {
  state.modal = null;
  state.otaProgress = 12;
  render();
  const timer = window.setInterval(() => {
    state.otaProgress += 14;
    if (state.otaProgress >= 100) {
      window.clearInterval(timer);
      state.otaProgress = 0;
      toast("Firmware installed", "Deck passed health confirmation; rollback slot preserved.");
      return;
    }
    render();
  }, 420);
}

app.addEventListener("click", (event) => {
  const target = event.target.closest("[data-action]");
  if (!target) return;
  const action = target.dataset.action;
  if (action === "navigate") navigate(target.dataset.page);
  else if (action === "mobile-nav") { state.mobileNav = !state.mobileNav; render(); }
  else if (action === "toggle-state") { state.stateInspector = !state.stateInspector; render(); }
  else if (action === "view-default") { state.viewState = "default"; render(); }
  else if (action === "close-modal") closeModal();
  else if (action === "close-modal-backdrop" && event.target === target) closeModal();
  else if (["pair-code", "revoke-device", "rotate-token", "ota-update", "add-provider", "stop-companion", "clear-wifi", "setup-revoke", "clear-history"].includes(action)) showModal(action);
  else if (action === "provider-filter") { state.providerFilter = target.dataset.filter; render(); }
  else if (action === "access-step") { state.accessStep = target.dataset.step; render(); }
  else if (action === "provider-editor-tab") { state.providerEditorTab = target.dataset.tab; render(); }
  else if (action === "session-filter") { state.sessionFilter = target.dataset.filter; render(); }
  else if (action === "history-range") { state.historyRange = target.dataset.range; render(); }
  else if (action === "preset-filter") { state.presetFilter = target.dataset.filter; render(); }
  else if (action === "select-device") { state.selectedDevice = target.dataset.device; render(); }
  else if (action === "backup-step") { state.backupStep = target.dataset.step; render(); }
  else if (action === "diagnostics-tab") { state.diagnosticsTab = target.dataset.tab; render(); }
  else if (action === "serial-mode") { state.serialMode = target.dataset.mode; render(); }
  else if (action === "toggle-web-tx") {
    state.webTx = !state.webTx;
    toast(state.webTx ? "Web TX lease acquired" : "Web TX lease released", state.webTx ? "This browser may transmit for 10 minutes; USB input is rejected." : "TX ownership returned to USB.", state.webTx ? "warning" : "success");
  }
  else if (action === "pause-terminal") { state.serialPaused = !state.serialPaused; render(); }
  else if (action === "send-serial") {
    const input = document.querySelector("#terminal-input");
    toast("Bytes sent", `${input?.value?.length || 0} characters accepted by the mock Web TX path.`);
  }
  else if (action === "download-serial") toast("Session export prepared", "Only the current in-memory serial session would be included.");
  else if (action === "setup-step") { state.setupStep = target.dataset.step; render(); }
  else if (action === "select-network") { state.setupNetwork = target.dataset.network; render(); }
  else if (action === "scan-wifi") toast("Network scan complete", "3 networks found; results refreshed without exposing saved passwords.");
  else if (action === "deck-screen") { state.deckScreen = target.dataset.screen; render(); }
  else if (["deck-next", "deck-key"].includes(action)) advanceDeck();
  else if (action === "deck-boot") { state.deckScreen = state.deckScreen === "serial" ? "codex" : "setup"; render(); }
  else if (action === "tray-state") { state.trayState = target.dataset.state; render(); }
  else if (action === "tray-toggle") { state.trayState = state.trayState === "running" ? "stopped" : "starting"; render(); }
  else if (action === "open-console") { state.trayState = "running"; navigate("overview"); toast("Console access granted", "Single-use desktop grant exchanged for a local session."); }
  else if (action === "login") { state.loggedIn = true; navigate("overview"); }
  else if (action === "test-provider") startProviderTest(target.dataset.provider);
  else if (action === "edit-provider" || action === "provider-details") navigate("provider-editor");
  else if (action === "toggle-lan") { state.lanManagement = !state.lanManagement; render(); }
  else if (action === "start-ota") startOTA();
  else if (action === "copy-code") { closeModal(); toast("Pairing details copied", "Code expires in five minutes and may be redeemed once."); }
  else if (action === "confirm-action" || action === "confirm-destructive") {
    const result = target.dataset.result || "Action completed";
    closeModal();
    toast(result, action === "confirm-destructive" ? "Destructive mock action completed after confirmation." : "Mock action completed.", action === "confirm-destructive" ? "warning" : "success");
  }
  else if (action === "refresh") toast("Snapshot refreshed", "All mock Provider and Deck timestamps are current.");
  else if (action === "export-diagnostics") toast("Diagnostics bundle ready", "Manifest and SHA-256 included; sensitive fields redacted.");
  else if (action === "save-calibration") toast("Deck confirmed calibration", "Temperature offset −4.0°C was written through and acknowledged.");
  else if (action === "clear-logs") toast("Local logs cleared", "Provider history and device settings were preserved.", "warning");
  else if (action === "ota-check") toast("No update available", "Stable channel is current.");
  else if (action === "import-provider") toast("Import preview opened", "cURL was parsed as data; no command was executed.");
  else if (action === "reorder-providers") toast("Deck order preview", "Drag-and-drop ordering would be saved only after confirmation.");
  else if (action === "save-provider") toast("Provider saved", "Request, mappings and display order passed validation.");
  else if (action === "copy-preview") toast("Redacted JSON copied", "Credential fields and account identifiers remain masked.");
  else if (action === "add-mapping") toast("Mapping row added", "Select a normalized field and enter a JSONPath.");
  else if (action === "export-history") toast("CSV export ready", "Only normalized hourly metrics are included.");
  else if (["new-preset", "edit-preset", "save-preset"].includes(action)) toast("Preset editor updated", "No bytes were sent; Web TX ownership is still required.");
  else if (action === "save-device") toast("Deck confirmed settings", "Device-owned values were acknowledged and are now active.");
  else if (action === "export-backup") toast("Encrypted backup created", "The age archive passed its local integrity check.");
  else if (action === "quit-companion") toast("Quit requested", "The real shell performs bounded listener shutdown first.", "warning");
});

app.addEventListener("change", (event) => {
  const target = event.target.closest("[data-action]");
  if (!target) return;
  if (target.dataset.action === "view-state") { state.viewState = target.value; render(); }
  else if (target.dataset.action === "history-provider") { state.historyProvider = target.value; render(); }
  else if (target.dataset.action === "update-channel") { state.updateChannel = target.value; toast("Update channel changed", `${target.options[target.selectedIndex].text} is now selected.`); }
});

window.addEventListener("keydown", (event) => {
  const isEditing = event.target instanceof Element && event.target.closest("input, textarea, select, [contenteditable]");
  if (event.key === "Escape" && state.modal) closeModal();
  if (isEditing || state.modal) return;
});

window.addEventListener("hashchange", () => {
  const hash = location.hash.replace(/^#\/?/, "");
  if (allPages.some((page) => page.id === hash) && hash !== state.page) {
    state.page = hash;
    render();
  }
});

render();
