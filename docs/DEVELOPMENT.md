# S3 RLCD Deck 开发文档

> 版本：0.3（M0 实现完成后的 M1–M5 执行基线）
> 更新日期：2026-08-13
> 目标硬件：Waveshare ESP32-S3-RLCD-4.2 / ESP32-S3-RLCD-DECK  
> 固件基线：ESP-IDF 6.0.2、LVGL 9.4.x  
> Companion 基线：Go、macOS 13+、Windows 11 x64

当前状态：本文定义 V1 的产品范围、交互、接口、安全边界、实现路线和验收标准。M0 功能代码已经合并，剩余物理/长稳发布证据由 #1 跟踪；M1–M5 分别由 GitHub 父规格 #20–#24 及其实现票推进。

## 1. 产品目标

S3 RLCD Deck 是一台持续通过 USB 供电的双用途桌面终端：

1. **AI 用量终端**：默认显示 Codex 的额度窗口、Token 和当前会话状态；通过 KEY 循环查看 Cursor、AIHubMix、DeepSeek 及用户配置的其他 Provider。
2. **串口工具**：长按 KEY 后启用目标 UART，通过 USB 转发到电脑，并通过同一套 Web 控制台提供实时终端；开发板屏幕只显示串口状态和统计，不显示串口正文。

设备的显示与串口实时链路运行在 ESP32-S3。需要访问本机应用状态、账户凭据或外部 API 的工作统一放在 Mac/Windows Companion。完整 Web 控制台也由 Companion 提供；开发板只保留首次配网、配对和恢复所需的极简页面。

## 2. 核心设计决策

| 主题 | 决策 |
| --- | --- |
| 固件框架 | ESP-IDF 6.0.2；正式工程不以 Arduino 为基线 |
| 显示 | LVGL 9.4.x、本地渲染、黑白横屏 400×300 |
| 默认页面 | Codex 用量与最近活跃会话 |
| AI 采集 | 全部在 Companion；ESP32 不持有 Provider 凭据 |
| Codex 状态 | 额度使用官方 App Server；外部会话只做旁路推测并标记 `INFERRED` |
| Cursor | 个人用量为实验性适配器，失效时明确显示 `STALE/UNAVAILABLE` |
| Companion | Go 单文件后台程序、系统托盘/菜单栏入口、内置静态 Web SPA |
| Web | 完整页面运行在 Companion；ESP32 只提供 Setup/Recovery 页面 |
| 设备链路 | ESP32 主动连接 Companion，使用证书指纹固定的 WSS |
| 串口启用 | AI 页面保持 `DISARMED`；进入串口页才初始化 UART |
| 默认 TX | 每次进入串口页均为 `USB TX`；Web 可临时切换成 `WEB TX` |
| 串口屏幕 | 只显示参数、速率、计数、错误和连接状态 |
| 持久记录 | 不持久保存串口正文；ESP32 和 Companion 只使用内存环形缓冲 |
| 配对 | 最多保存 5 个 Companion；同时只有一个 Active Connection |
| 配置迁移 | 支持带密码的 `age` 加密备份，用户录入的 API Key 可包含在备份中 |
| 升级 | 用户确认的签名 OTA、A/B 分区、失败回滚；不做静默升级 |

## 3. 系统架构

```mermaid
flowchart LR
    subgraph PC["Mac / Windows Companion"]
        C1["AI Collectors\nCodex / Cursor / HTTP Providers"]
        C2["Normalizer + SQLite History"]
        C3["Web SPA\nAI / Sessions / Serial / Settings"]
        C4["Device Hub\nPairing + WSS"]
        C1 --> C2
        C2 --> C3
        C2 --> C4
        C3 --> C4
    end

    subgraph Deck["ESP32-S3-RLCD-DECK"]
        E1["AI Snapshot Cache"]
        E2["LVGL 400×300"]
        E3["UART Router"]
        E4["USB Serial/JTAG Bridge"]
        E5["Setup / Recovery Web"]
        E1 --> E2
        E3 --> E2
        E3 --> E4
    end

    O["Codex App Server / Local Session Files"] --> C1
    R["Cursor Local State / Experimental Endpoint"] --> C1
    A["AIHubMix / DeepSeek / Custom HTTPS API"] --> C1
    C4 <-->|"Pinned WSS\nAI + Config + Serial"| Deck
    T["Target UART"] <--> E3
    E4 <--> H["Computer COM Port"]
```

### 3.1 两条运行链路

AI 链路允许短时离线：

```text
Provider / 本机工具
        ↓
Companion Collector → Normalizer → WSS Snapshot
                                      ↓
                              ESP32 Cache → LVGL
```

串口链路必须实时且隔离慢消费者：

```text
Target UART → UART RX → Router
                         ├─ USB sink
                         ├─ Companion WSS sink
                         ├─ ESP32 512 KiB ring
                         └─ Screen statistics
```

UI、Wi-Fi、Web 客户端或 USB 主机变慢时，只允许对应 sink 丢弃自己的最旧数据，不能反向阻塞 UART RX。

### 3.2 信任边界

- Codex/Cursor 登录状态、API Key 和自定义 Provider 密钥只存在 Companion。
- ESP32 只接收显式白名单字段构成的显示快照。
- 完整管理 Web 默认只在电脑回环地址开放。
- 设备入口与管理入口使用不同监听端口、Token 和权限。
- 串口正文不进入 AI 历史、诊断日志或设备持久存储。

## 4. 硬件基线

### 4.1 板卡资源

| 项目 | 基线 |
| --- | --- |
| 主控 | ESP32-S3-WROOM-1-N16R8，双核 LX7，最高 240 MHz |
| 内存 | 8 MB PSRAM |
| Flash | 16 MB |
| 显示 | 4.2 英寸反射式 RLCD，物理 300×400；项目使用横屏 400×300 |
| 网络 | 2.4 GHz Wi-Fi、BLE 5 LE；V1 只使用 Wi-Fi |
| 传感器 | SHTC3 温湿度传感器 |
| RTC | PCF85063ATL，外部 32.768 kHz 晶体 |
| 存储接口 | 板载 TF 接口不参与 V1 |
| 操作 | KEY、BOOT、PWR；无触摸 |
| 供电 | 设计假设设备始终通过 USB 或稳定外部电源供电 |

屏幕没有背光且只有黑白表达能力。状态必须通过文字、边框、填充纹理和图标共同表达，不能依赖颜色。

### 4.2 已使用引脚

微雪 LVGL V9 示例和板级资料中的主要引脚：

| 功能 | GPIO / 地址 |
| --- | --- |
| RLCD DC | GPIO5 |
| RLCD CS | GPIO40 |
| RLCD SCK | GPIO11 |
| RLCD MOSI | GPIO12 |
| RLCD RST | GPIO41 |
| RLCD TE | GPIO6 |
| I²C SDA | GPIO13 |
| I²C SCL | GPIO14 |
| SHTC3 | I²C `0x70` |
| PCF85063 | I²C `0x51`，RTC_INT 为 GPIO15 |
| KEY | GPIO18 |
| BOOT | GPIO0 |
| USB D- / D+ | GPIO19 / GPIO20 |
| 目标 UART RX | 扩展口 RXD / GPIO44 |
| 受控目标 UART TX | GPIO17 |

扩展排针当前映射：

```text
上排：VBUS  GND  GPIO19  GPIO20  TXD  RXD  SDA  SCL
下排：3V3   GND  GPIO0   GPIO1   GPIO2 GPIO3 GPIO17 GPIO18
```

丝印 `TXD/RXD` 是 UART0 信号，ESP32-S3 常用映射为 U0TXD/GPIO43、U0RXD/GPIO44。U0TXD 在复位阶段可能输出 ROM 启动日志，因此它不能满足“AI 页面绝不向目标设备发送”的严格要求。V1 使用 RXD/GPIO44 接收，使用空闲 GPIO17 作为应用控制的目标 TX；丝印 TXD 默认不连接目标设备。正式转接板投产前需要再次按实际板卡版本核对原理图和导通。

### 4.3 UART 接线

只接收：

```text
目标设备 TX  ─────>  RLCD-DECK RXD
目标设备 GND ─────>  RLCD-DECK GND
```

双向桥接：

```text
目标设备 TX  ─────>  RLCD-DECK RXD
目标设备 RX  <─────  RLCD-DECK GPIO17（受控 TX）
目标设备 GND ─────>  RLCD-DECK GND
```

电气规则：

- 不连接两块板的电源脚。
- 仅可直接连接 3.3 V TTL UART。
- 1.8 V 或 5 V UART 必须使用电平转换。
- RS-232/DB9 必须通过 MAX3232 一类收发器。
- 不需要向目标设备发送时，物理上只连接 RXD 和 GND。
- 不把丝印 TXD/U0TXD 连接到目标 RX；它可能包含芯片复位日志。
- 目标串口使用 `UART_NUM_1`，经 GPIO matrix 映射 RXD/GPIO44 和 GPIO17，避免复用 UART0 的应用日志通道。
- GPIO17 在启动和 AI 页面保持输入/高阻；只有进入串口页并安装 UART driver 后才配置为 TX。
- 退出串口页时卸载 driver，并立即把 GPIO17 恢复为输入/高阻。

## 5. 屏幕信息架构

### 5.1 全局状态栏

顶部固定显示：

- 本地时间。
- 经偏移校准的 SHTC3 温度。
- Wi-Fi 信号。
- Active Companion 在线状态。

湿度、设备 IP、固件版本和详细诊断放在 Web System 页面，不占用顶部空间。

### 5.2 Codex 默认首页

```text
┌──────────────────────────────────────────────┐
│ 10:36  24.8°C   Wi-Fi ▮▮▮   AGENT ●          │
├──────────────────────────────────────────────┤
│ CODEX                         INFERRED        │
│ Primary  [██████░░░░]  62% left   02:44     │
│ Weekly   [████░░░░░░]  78% left   Tue       │
├──────────────────────────────────────────────┤
│ s3-rlcd-deck     RUNNING?       12m          │
│ 146k tokens      Context 41%     +2 sessions │
└──────────────────────────────────────────────┘
```

显示优先级：

1. Codex 数据状态及可信度。
2. App Server 返回的动态额度窗口、剩余比例和重置时间。
3. 最近活跃会话的脱敏名称、状态、持续时间和本次 Token。
4. 其他活跃会话数量。
5. Credits 仅在官方接口实际返回时显示。

不把窗口名称或时长写死为固定产品规则；按接口返回动态渲染。

### 5.3 其他 Provider 页面

KEY 按用户配置顺序循环所有已启用 Provider：

```text
┌──────────────────────────────────────────────┐
│ 10:36  24.8°C   Wi-Fi ▮▮▮   AGENT ●          │
├──────────────────────────────────────────────┤
│ CURSOR                         EXPERIMENTAL   │
│ Balance       $18.42                          │
│ Usage         [███████░░░] 71%               │
│ Reset         2026-08-16                     │
│ Updated       10:35:42                       │
├──────────────────────────────────────────────┤
│ WEB 192.168.1.20:7777                        │
└──────────────────────────────────────────────┘
```

布局规则：

- 固定显示 Provider 名称、状态、更新时间。
- 优先显示余额和货币；存在额度周期时再显示百分比和重置时间。
- 只有 Token 或调用次数时，改为相应指标。
- 缺失字段隐藏，不用 `0` 冒充未知值。
- `STALE/UNAVAILABLE` 使用固定状态区域，避免布局跳动。
- 完整 Web 未开放局域网访问时显示 `WEB ON COMPUTER`；开放后显示可访问的 IPv4/mDNS 地址。
- Companion 离线时显示 `AGENT OFFLINE` 和开发板恢复页地址。

### 5.4 串口状态页

```text
┌──────────────────────────────────────────────┐
│ SERIAL          USB TX             00:18:32  │
├──────────────────────────────────────────────┤
│ 115200  8N1                                  │
│ RX  12,482,109 B     11.3 KiB/s              │
│ TX      18,420 B        0.2 KiB/s            │
│ UART ERR 0    ESP OVERWRITE 0                │
│ USB ●         AGENT ●       WEB CLIENTS 1    │
├──────────────────────────────────────────────┤
│ KEY: Stats    BOOT: Exit       Refresh 1s    │
└──────────────────────────────────────────────┘
```

屏幕不接收或保存用于显示的串口文本，只显示：

- `DISARMED / ARMED RX / USB TX / WEB TX`。
- 波特率、数据位、校验和停止位。
- 本会话 RX/TX 总量和实时速率。
- UART overflow、ESP 缓冲覆盖和 Companion 缓冲覆盖。
- USB、Companion、Web 客户端连接状态。
- 会话持续时间和刷新频率。

屏幕刷新档位为 0.5、1、2、5、10 秒，默认 1 秒；只影响屏幕统计，不影响底层数据流。

### 5.5 首次启动与离线状态

| 条件 | 屏幕行为 |
| --- | --- |
| 首次启动 | Setup 页面显示临时 AP、随机密码和恢复页地址 |
| Wi-Fi 已连接但未配对 | `PAIR COMPANION` |
| Companion 离线不足 24 小时 | 显示最后快照、时间戳和 `STALE` |
| Companion 离线超过 24 小时 | 隐藏额度数字，显示 `AGENT OFFLINE` 和恢复页地址 |
| 配对成功并收到快照 | 自动进入 Codex 首页 |

## 6. 按键与状态机

### 6.1 AI 页面

| 操作 | 行为 |
| --- | --- |
| KEY 短按 | `Codex → Provider 1 → Provider 2 → … → Codex` |
| KEY 长按 1.5 秒 | 进入串口页并初始化 UART |
| BOOT 短按 | 无操作 |
| BOOT 长按 3 秒 | 启动临时配置 AP |

即使没有其他 Provider，也保留一个配置提示页，用于显示 Web/恢复地址。

### 6.2 串口页面

| 操作 | 行为 |
| --- | --- |
| KEY 短按 | 切换串口统计子视图 |
| BOOT 短按 | 停止串口会话、撤销 TX、返回 Codex 首页 |

串口模式状态机：

```mermaid
stateDiagram-v2
    [*] --> DISARMED
    DISARMED --> USB_TX: KEY 长按进入串口页
    USB_TX --> WEB_TX: Web 管理员开启 WEB TX
    WEB_TX --> USB_TX: 开关关闭 / Lease 超时 / Web 断开
    USB_TX --> DISARMED: BOOT 退出
    WEB_TX --> DISARMED: BOOT 退出
```

规则：

- 进入串口页即 `ARMED`，默认 Owner 为 `USB TX`。
- Web 管理员可以在页面上切换到 `WEB TX`，无需设备再次确认。
- 切换到 `WEB TX` 时清空尚未发送的 USB TX 队列。
- `WEB TX` 期间电脑 USB 输入被拒绝并累计 `usb_tx_rejected`。
- 同时只允许一个已登录浏览器持有 Web TX Lease，默认 10 分钟并通过心跳续期。
- Web 页面关闭、心跳中断、Lease 超时或关闭开关时自动恢复 `USB TX`。
- 退出串口页时立即停止 UART、撤销 Owner、清空待发送队列。
- 下次进入串口页无条件从新的 `USB TX` 会话开始。

注意：按住 BOOT 的同时复位仍会进入芯片下载模式；运行时长按和复位期间按住不是同一行为。

## 7. AI 采集方案

详细的开源项目核验见 [AI 用量采集方案对比](research/ai-usage-collector-comparison.md)。

### 7.1 选型结论

没有单个项目同时满足跨平台、Codex/Cursor、精确外部会话状态、只读凭据和安全设备接口。V1 采用组合方案：

| 层 | 方案 |
| --- | --- |
| 跨平台采集主体 | 选择性复用 OpenUsage 的 Go Provider、平台检测和打包思路 |
| Codex 额度 | 官方 Codex App Server first |
| 外部 Codex 会话 | 参考 abtop，以进程和 JSONL 做只读旁路推测 |
| 历史聚合校验 | 参考 Tokscale |
| 配对和第三方余额契约 | 参考 Token Monitor |
| 兼容/UI 参考 | TokenTracker；不复用其 Codex 凭据刷新写回逻辑 |

所有复用代码必须固定到经过核验的提交、保留许可证声明并建立上游变更检查。不得把任何候选项目的宽泛原始响应直接发送给 ESP32。

### 7.2 Codex 官方数据与边界

Companion 使用 Codex App Server 的结构化能力：

- `account/rateLimits/read`：额度桶、使用比例、窗口和重置时间。
- `account/rateLimits/updated`：额度变化通知。
- `account/usage/read`：Token 活动和聚合统计。
- `thread/*`、`turn/*`：只对同一 App Server 实例拥有或加载的线程提供准确事件。

V1 不接管用户的 Codex Desktop、IDE 或 CLI 会话，因此另启的 App Server 不能被描述为全局任务注册表。会话状态分级：

| source | confidence | 允许显示的状态 |
| --- | --- | --- |
| `codex_app_server_owned` | `verified` | Running、Waiting approval、Waiting input、Completed、Failed |
| `process_jsonl_observer` | `inferred` | Running、Recent、Ended、Unknown |
| `none` | `unavailable` | Unavailable |

强制规则：

- `waitingOnApproval` 只能来自官方事件或 active flags。
- 仅凭文件停止增长不能显示“等待审批”，只能显示 Recent/Unknown。
- Windows 上进程与会话文件无法唯一匹配时降级为 Unknown。
- 不上传 Prompt、回复、命令输出、工具参数或绝对路径。
- 会话标题默认隐藏；屏幕只显示用户别名或工程目录最后一级。

### 7.3 Cursor

个人 Cursor 用量没有公开稳定的官方 API。V1 适配器遵循：

- `experimental=true`。
- 只读访问本机 Cursor 数据和 access token。
- 不读取 refresh token，不主动刷新，不写回数据库或系统 Keychain。
- 私有 endpoint 和响应 schema 单独版本化并做严格校验。
- 失败只影响 Cursor 页面，不阻断其他 Provider、Web 或串口功能。
- 启用过的 Cursor 页面在失败时保留，显示旧值 `STALE` 或 `UNAVAILABLE`。
- 若未来出现个人官方 API，优先迁移。

### 7.4 AIHubMix、DeepSeek 与通用 Provider

Companion 提供内置模板，同时提供结构化 HTTP Provider：

- GET/POST。
- HTTPS URL。
- Headers 和 JSON Body。
- 余额、已用量、总额度、重置时间和货币的 JSONPath 映射。
- 1、5、15、30、60 分钟刷新档位。
- `Test Request` 脱敏预览。
- 从 curl 文本导入结构化字段。

边界：

- 不执行 curl、Shell、JavaScript 或任意插件代码。
- 远程 Provider 强制 HTTPS；HTTP 仅允许私有局域网地址并显示警告。
- 禁止重定向到不同主机。
- 默认超时 10 秒，响应体上限 256 KiB。
- 响应解析失败返回明确错误，不沿用错误字段。

### 7.5 统一 Provider DTO

```json
{
  "schema_version": 1,
  "provider_id": "codex",
  "display_name": "Codex",
  "status": "ok",
  "source": "codex_app_server",
  "confidence": "verified",
  "experimental": false,
  "updated_at": "2026-08-09T10:36:05Z",
  "stale_after_seconds": 120,
  "balance": null,
  "currency": null,
  "windows": [
    {
      "name": "primary",
      "used_percent": 38.0,
      "remaining_percent": 62.0,
      "window_minutes": 300,
      "resets_at": "2026-08-09T13:20:00Z"
    }
  ],
  "tokens": {
    "input": 120000,
    "cached_input": 45000,
    "output": 18000,
    "reasoning": 8000,
    "total": 146000
  },
  "error": null
}
```

未知值使用 `null`，不能使用零代替。ESP32 对未知字段前向兼容，对未知 schema major version 拒绝并保留最后有效快照。

### 7.6 会话 DTO

```json
{
  "session_id": "anonymous-stable-id",
  "display_name": "s3-rlcd-deck",
  "state": "running",
  "confidence": "inferred",
  "started_at": "2026-08-09T10:24:00Z",
  "last_activity_at": "2026-08-09T10:36:01Z",
  "duration_seconds": 721,
  "turn_tokens": 18420,
  "context_used_percent": 41.0
}
```

不得包含原始线程标题、Prompt、完整工程路径、文件名、命令或工具参数。

## 8. Companion 设计

### 8.1 交付形态

- Go 单文件后台程序。
- macOS 菜单栏和 Windows 托盘入口：打开控制台、显示连接状态、启动/停止、退出。
- 内置编译后的 Web SPA，不依赖公网 CDN。
- 开发阶段可前台运行；V1 提供 macOS LaunchAgent 和 Windows Task Scheduler 登录自启动。
- macOS 13+ 支持 Apple Silicon，保留 Intel 构建。
- Windows 11 x64 为正式目标，Windows 10 x64 best-effort。

V1 不引入 Electron。若未来需要独立原生窗口，可在同一 SPA 外增加轻量 WebView 外壳，不改变后端接口。

### 8.2 推荐模块

```text
companion/
├── cmd/s3deck-companion/
├── internal/
│   ├── providers/
│   │   ├── codex/
│   │   ├── cursor/
│   │   └── httpbalance/
│   ├── sessions/          # App Server + passive observer
│   ├── normalize/         # Provider / session DTO
│   ├── history/           # SQLite, retention, CSV
│   ├── secrets/           # Keychain / Credential Manager
│   ├── backup/            # age export/import
│   ├── devices/           # pairing, WSS, profiles
│   ├── serialhub/         # 8 MiB ring, browser fan-out
│   ├── web/               # management API + embedded SPA
│   ├── diagnostics/       # redaction + rotation
│   └── config/
└── web/
    ├── src/
    └── dist/              # go:embed
```

### 8.3 Web 与设备监听

| 入口 | 默认绑定 | 权限 |
| --- | --- | --- |
| 管理 Web | `127.0.0.1:7777` | 完整 UI、配置、历史、备份、串口控制 |
| Device Hub | 选定 LAN 地址/独立端口 | 配对后的 WSS、最小健康接口 |
| 可选 LAN Web | 用户显式开启 | 完整 UI，仍需管理员登录 |

要求：

- 管理和设备 Token 不复用。
- 管理写接口执行 Origin/CSRF 校验。
- Device Hub 设置请求大小、读取/写入超时、IP 限速。
- 未完成配对的设备不能获取快照或发送串口数据。
- 局域网管理未开启时，屏幕显示 `WEB ON COMPUTER`。

### 8.4 本地存储

Companion 使用本地 SQLite 保存最近 90 天的小时级用量、余额和额度快照：

- 不保存 Prompt、回复或串口原文。
- 支持 CSV 导出和一键清空。
- 用户可关闭历史记录。
- 超过 90 天自动删除。
- 数据库迁移带 schema version，并在升级前创建安全备份。

### 8.5 凭据

- macOS 使用 Keychain。
- Windows 使用 Credential Manager。
- 配置文件只保存 secret reference ID。
- Access token 失效时返回 `AUTH_STALE`，提示用户回到原应用重新登录。
- Companion 不刷新、不轮换、不写回 Codex/Cursor 拥有的认证文件。

### 8.6 加密导出与导入

使用带密码的 `age` 加密备份，包含：

- 用户录入的 Provider 配置和 API Key。
- Provider 顺序、刷新频率和 Web 设置。
- Companion 应用设置。
- 设备资料和硬件配置缓存副本。

不包含：

- Codex/Cursor 自动发现的 OAuth、Cookie、access/refresh token。
- Companion 与设备之间的配对 Token。
- Web 登录会话。
- SQLite 历史数据库。
- 串口缓冲。

导入提供：

- 默认“合并”，冲突逐项确认。
- 明确警告后的“替换”。
- “仅导入 Provider”。
- 导入前脱敏预览。
- 错误密码、损坏文件或不支持的 schema 必须安全失败，不产生半导入状态。

## 9. 多 Companion 与配对

### 9.1 配对流程

1. Companion 生成一次性六位配对码并启动 Device Hub。
2. 用户长按 BOOT 打开开发板临时 AP/恢复页。
3. 在恢复页输入 Companion 地址和一次性配对码。
4. 双方交换独立设备 Token、Companion 证书指纹和设备 ID。
5. ESP32 固定证书指纹，以后只连接匹配的 WSS 服务。
6. 配对成功后关闭临时 AP。

每个 Companion 使用独立 Token 和证书，可单独撤销。

### 9.2 多配对规则

- 每块开发板最多保存 5 个 Companion profile。
- Profile 包含名称、地址、设备 Token、证书指纹、优先级和最后成功时间。
- 同一时间只连接一个 Active Companion。
- Active Companion 离线超过 30 秒后，按优先级连接下一个。
- 切换成功后保持粘性；原高优先级设备恢复也不抢占。
- 用户可在恢复页手动选择 Active Companion。
- Companion 之间不自动同步配置；使用加密备份手工迁移。

## 10. 设备协议

### 10.1 WSS 连接

ESP32 主动连接 Companion，便于处理设备 DHCP 地址变化和电脑防火墙规则。连接建立后先发送 `device.hello`：

```json
{
  "type": "device.hello",
  "protocol_version": 1,
  "device_id": "deck-01",
  "firmware_version": "0.1.0",
  "board": "esp32-s3-rlcd-4.2",
  "capabilities": ["display", "serial", "ota"],
  "serial_state": "disarmed"
}
```

Companion 验证 Token、证书连接、device ID 和协议版本后返回配置摘要与最新 AI 快照。

### 10.2 消息类型

控制消息使用带 `type` 和版本的 JSON：

| 消息 | 方向 | 用途 |
| --- | --- | --- |
| `device.hello` | ESP → Companion | 版本、能力、状态 |
| `device.heartbeat` | 双向 | 在线检测、时钟、队列水位 |
| `snapshot.ai` | Companion → ESP | 规范化 Provider 与会话快照 |
| `config.get/set/result` | 双向 | 权威配置读写和确认 |
| `serial.state` | ESP → Companion | 模式、Owner、统计、错误 |
| `serial.owner.request/result` | Companion ↔ ESP | USB/WEB Owner 切换 |
| `serial.history.request` | Companion → ESP | 网络重连后请求最近数据 |
| `ota.offer/chunk/result` | Companion ↔ ESP | 用户确认后的 OTA |
| `error` | 双向 | 结构化错误 |

串口原始流使用 WebSocket binary frame，至少带通道、序号、设备单调时钟和 payload length。原始字节不经过 UTF-8 转换。

### 10.3 AI 快照

```json
{
  "type": "snapshot.ai",
  "protocol_version": 1,
  "generated_at": "2026-08-09T10:36:18Z",
  "timezone": "Asia/Shanghai",
  "provider_order": ["codex", "cursor", "deepseek"],
  "providers": [],
  "codex_sessions": [],
  "next_refresh_seconds": 5
}
```

- Provider 间错误隔离。
- 未变化的快照可以只发送版本/摘要，避免无意义重绘。
- ESP32 拒绝过大的快照和无效时间戳。
- 所有时间在线路上使用 UTC。

## 11. Web 控制台

### 11.1 侧边栏

| 页面 | 功能 |
| --- | --- |
| Overview | 设备、Codex/Cursor/其他 Provider 汇总 |
| AI Providers | Provider 配置、凭据、顺序、轮询、测试请求 |
| Codex Sessions | 会话列表、可信度、时间、匿名统计 |
| Serial Terminal | 实时终端、HEX、TX Owner、缓冲下载 |
| Network | Companion 地址、设备配对、Wi-Fi/连接状态 |
| System | 时间、温度、固件、OTA、诊断、备份、重启 |

SPA 静态资源构建后嵌入 Go 二进制，不引用公网字体、脚本或 CDN。

### 11.2 串口终端开源组件

- 使用 `xterm.js` 作为文本/ANSI、Unicode、滚动、搜索和终端渲染核心。
- 复用 `pineTERM` 的 MIT 许可 HEX 显示、HEX 输入、日志导出和预设命令设计。
- 把原项目的 Web Serial transport 替换为 Companion WebSocket transport。
- 自研范围只包括传输适配、设备协议、TX Owner 和产品 UI 集成；不自行实现 ANSI 终端模拟器。

查看方式：

- Text/ANSI。
- HEX。
- Text + HEX。

发送方式：

- 文本及 CR/LF 选项。
- HEX 字节。
- 用户保存的预设命令。

浏览器层负责显示转换，Companion 和 ESP32 始终保留原始字节。

### 11.3 多浏览器

- 允许多个已登录浏览器同时只读观察。
- 同时只有一个浏览器持有 WEB TX Lease。
- Lease 默认 10 分钟，心跳续期。
- 浏览器关闭或失联时释放 Lease，ESP32 自动恢复 USB TX。

## 12. 串口实现

### 12.1 参数

| 参数 | 支持 |
| --- | --- |
| 波特率 | 1200～921600，常用预设 + 自定义 |
| 数据位 | 7、8 |
| 校验 | None、Even、Odd |
| 停止位 | 1、2 |
| 默认 | 115200 8N1 |
| 硬件流控 | 不支持 |
| RX / TX 引脚 | GPIO44 / GPIO17 |

V1 正式保证 115200。更高速率只有通过相同压力测试后才在 Web 中标记 `VERIFIED`。

### 12.2 Router 与背压

```text
UART driver event queue
        ↓
serial_rx → fixed blocks → serial_router
                              ├─ usb_ring       16～64 KiB
                              ├─ wss_ring       16～64 KiB
                              ├─ history_ring   512 KiB PSRAM
                              └─ stats events   latest-only
```

- 不让多个消费者竞争弹出同一个队列。
- Router 为每个 sink 管理独立 ring 和 drop counter。
- Sink 满时丢该 sink 最旧块，不能等待。
- UART FIFO overflow/driver buffer full 是全局严重错误，必须置顶显示。
- 屏幕事件只保留最新统计。
- 固定块池避免逐字节动态分配。

### 12.3 双层历史缓冲

| 位置 | 大小 | 生命周期 | 用途 |
| --- | --- | --- | --- |
| ESP32 PSRAM | 默认 512 KiB，可配置 64～2048 KiB | 当前串口会话 | Companion 短暂断线恢复 |
| Companion RAM | 8 MiB | 当前串口会话 | 浏览器重连、搜索、多观察者、下载 |

覆盖最旧数据时累计 `overwritten_bytes`。串口会话结束或 Companion 退出时清空。Web 下载只包含当前会话。

### 12.4 USB

V1 使用 ESP32-S3 USB Serial/JTAG 的单 CDC 通道作为目标串口桥：

- 发布固件的目标串口流不与 ESP-IDF console 日志混用；发布配置关闭 ESP-IDF console。
- 发布固件诊断走 Web/内存诊断环。开发/HIL 构建可临时启用结构化 USB `boot_ok` 与无凭据的 `deck_build_identity`，仅用于启动和构建身份验收，并在发布配置中关闭。
- 目标 UART 不使用 U0TXD；USB console/ROM 输出不会通过 GPIO17 注入目标设备。
- USB sink 独立任务处理，不能从 UART RX 同步写 USB。
- 电脑未打开端口时只允许 USB sink 自身丢弃。
- 不烧不可逆 USB eFuse。
- 设备复位产生的 ROM USB 启动文字不作为 V1 问题处理；正常工作时先启动设备，再进入串口页。
- 关闭可能让 USB 断开的睡眠模式。

发布版若未来需要双 CDC 或从枚举开始完全隔离启动文字，再单独评估 TinyUSB；不在 V1 同时维护两套 USB 方案。

## 13. 固件架构

### 13.1 组件

```text
firmware/
├── main/
├── components/
│   ├── board_support/       # 引脚、RLCD、按键、RTC、SHTC3
│   ├── display/             # 1bpp framebuffer、异步 panel 提交
│   ├── application_ui/      # 唯一 LVGL owner、ViewModel 与页面
│   ├── companion_link/      # pairing、WSS、协议、failover
│   ├── snapshot_store/      # 校验、缓存、stale 规则
│   ├── serial_service/      # UART、Router、统计、Owner
│   ├── usb_bridge/          # USB Serial/JTAG sink/source
│   ├── setup_web/           # 最小配网和恢复页面
│   ├── settings/            # NVS、版本和迁移
│   ├── ota_service/         # 签名、A/B、回滚
│   └── health/              # 看门狗、堆、队列和诊断
└── sdkconfig.defaults
```

### 13.2 任务与所有权

| 任务 | 职责 | 关键约束 |
| --- | --- | --- |
| `ui` | 唯一 LVGL owner | 其他任务只能发送 ViewModel/事件 |
| `companion_link` | WSS、心跳、快照、配置、重连 | Provider 错误不能重启链路 |
| `serial_rx` | UART driver queue → blocks | 串口会话中的最高业务优先级 |
| `serial_router` | USB/WSS/history/stats 扇出 | 不等待慢 sink |
| `usb_sink/source` | 电脑 COM 双向数据 | Owner 不是 USB 时拒绝电脑输入 |
| `wss_serial_sink/source` | Companion 双向原始流 | Owner 不是 WEB 时拒绝 Web TX |
| `setup_web` | AP 和恢复页 | 仅恢复模式运行 |
| `health` | 看门狗、内存、队列水位 | 生成脱敏诊断 |

AI 页面下不创建活跃 UART 数据路径；进入串口页后再安装/启动 UART driver 和相关任务，退出时释放。

### 13.3 配置所有权

| 配置 | 权威端 |
| --- | --- |
| Provider、API Key、顺序、轮询 | Companion |
| Codex 隐私、历史、Web 用户 | Companion |
| Wi-Fi、Companion profiles | ESP32 |
| UART 参数、缓冲大小、屏幕刷新 | ESP32 |
| 温度偏移 | ESP32，Companion Web 写入并等待确认 |
| 时区 | Companion 权威，ESP32 缓存 |

Web 修改 ESP32 配置时使用 write-through：只有收到设备确认才显示成功。Companion 的缓存副本不在重连时擅自覆盖设备。

### 13.4 快照持久化

- ESP32 保存最后一个规范化 AI 快照。
- 使用两个交替 NVS blob、版本号、长度和 CRC，写完校验后切换 active marker。
- 最多每 30 分钟写一次，减少 Flash 磨损。
- 解析失败或 schema 不兼容不能覆盖最后有效快照。
- 离线不足 24 小时显示旧数据和 `STALE`；超过 24 小时隐藏额度。

### 13.5 时间与温度

- 协议和数据库统一保存 UTC。
- 显示时按 Companion 配置时区转换，默认 `Asia/Shanghai`。
- ESP32 联网后使用 SNTP 校时，并写入 PCF85063 RTC。
- RTC 未校准时显示 `--:--`，不使用编译时间或硬编码时间。
- SHTC3 温度偏移可配置，初始值采用微雪示例的 `-4°C`，装壳后重新校准。
- 完全断电后的 RTC 保持依赖板卡 J7 外接备份电源，V1 不作持续走时承诺。

### 13.6 Setup AP

- BOOT 长按 3 秒或首次启动时开启。
- 每次生成随机热点密码并显示在屏幕上。
- 10 分钟无操作自动关闭，配置成功立即关闭。
- 恢复页只允许 Wi-Fi、Companion 配对、Active Companion 和基础硬件设置。
- 恢复页不能查看 Provider API Key。

### 13.7 OTA

- Companion Web 检查/选择固件，用户明确确认后上传。
- ESP32 使用 A/B OTA 分区和 boot rollback。
- 固件校验数字签名、完整性和板卡型号。
- 首次启动新固件完成健康检查后才标记有效。
- OTA 中断、签名失败或健康检查失败时保留/回滚旧固件。
- BOOT/UART 烧录始终作为恢复路径。

## 14. 安全与隐私

### 14.1 强制规则

- ESP32 永不接收 Provider API Key、Codex/Cursor Cookie 或 OAuth token。
- Companion 不改写 Codex/Cursor 认证文件或 Keychain 项目。
- 设备 WSS 使用每个 Companion 独立的 Token 和固定证书指纹。
- Token 使用常量时间比较，可轮换、可逐设备撤销。
- 管理写操作要求登录、CSRF/Origin 校验和合理速率限制。
- 会话输出只使用匿名 ID、脱敏名称和统计。
- 不发送匿名遥测、崩溃报告或使用统计到外部服务。

### 14.2 诊断日志

Companion 保存脱敏轮转日志，默认保留 7 天或最多 50 MiB：

- 禁止记录 Authorization、Cookie、API Key。
- 禁止记录 Prompt、回复、工具参数和串口正文。
- 本地路径只保留必要的最后一级或哈希。
- Provider 响应只记录状态码、耗时、schema 版本和脱敏错误。
- 支持导出脱敏诊断包。

ESP32 发布固件只维护小型内存诊断环，通过已登录的 System 页面读取。开发/HIL 构建允许启用不含凭据或用户数据的结构化 USB 启动事件。

## 15. 开发环境

### 15.1 当前基线

- ESP-IDF：`/Users/xiaowang/.espressif/v6.0.2/esp-idf`
- 已验证版本：ESP-IDF v6.0.2
- LVGL：锁定 9.4.x
- 芯片目标：ESP32-S3
- 当前观察到的设备口：`/dev/cu.usbmodem1101`，实际名称可能变化
- VS Code 使用 Espressif ESP-IDF 扩展，烧录方式为 UART

```bash
source /Users/xiaowang/.espressif/v6.0.2/esp-idf/export.sh
idf.py --version
idf.py set-target esp32s3
idf.py build
idf.py -p /dev/cu.usbmodem1101 flash monitor
```

注意：

- 不使用普通 CMake Tools 配置 ESP-IDF；建议 `cmake.configureOnOpen=false`。
- ESP-IDF 管理 Ninja；缺少 `build.ninja` 通常意味着配置失败。
- 烧录/监视前关闭其他串口工具。
- 当前已验证的是 UART 烧录路径，不把 JTAG 作为默认设置。

### 15.2 微雪 LVGL V9 示例兼容补丁

本机使用 ESP-IDF 6.0.2 构建微雪 `02_Example/ESP-IDF/09_LVGL_V9_Test` 时，依赖解析和 LVGL 9.4.0 可以进行，但显示驱动的 GPIO 类型需要修正：

```cpp
io_config.dc_gpio_num = dc_;
io_config.cs_gpio_num = cs_;
```

应把 `DisplayPort` 的 GPIO 参数和成员统一改为 `gpio_num_t`：

```cpp
DisplayPort(
    gpio_num_t mosi,
    gpio_num_t scl,
    gpio_num_t dc,
    gpio_num_t cs,
    gpio_num_t rst,
    int width,
    int height,
    spi_host_device_t spihost = SPI3_HOST);
```

临时最小转换：

```cpp
io_config.dc_gpio_num = static_cast<gpio_num_t>(dc_);
io_config.cs_gpio_num = static_cast<gpio_num_t>(cs_);
```

正式工程采用类型正确的成员定义，不通过降低编译严格性掩盖问题。

## 16. 推荐仓库结构

```text
s3-rlcd-deck/
├── firmware/
│   ├── main/
│   ├── components/
│   ├── partitions.csv
│   └── sdkconfig.defaults
├── companion/
│   ├── cmd/
│   ├── internal/
│   ├── web/
│   └── go.mod
├── protocol/
│   ├── schema/
│   └── fixtures/
├── hardware/
│   └── uart-adapter/
├── tests/
│   ├── integration/
│   └── hardware-in-loop/
└── docs/
    ├── DEVELOPMENT.md
    └── research/
```

协议 fixtures 同时供 Go 和 ESP-IDF 测试使用，防止两端各自演化出不兼容字段。

## 17. 实施里程碑

### M0：板级与 UI 基线

- ESP-IDF/LVGL 工程。
- 修正微雪 RLCD 驱动兼容问题。
- 400×300 黑白基准页、中文字体和刷新验证。
- KEY/BOOT、SHTC3、PCF85063。
- Setup/Recovery 页面。

完成标准：连续显示 24 小时，无异常重启；按键短按/长按可靠；时间无效状态正确。

### M1：Companion 与配对

跟踪规格：[#20](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/20)

- Go 程序骨架、托盘/菜单栏、嵌入 SPA。
- 管理入口与 Device Hub 分离。
- 一次性码配对、证书指纹固定、WSS 心跳。
- 多 Profile 数据结构先落地，故障切换可在 M5 完成。
- 实板验收时控制 Mac 始终保留在普通 LAN；由有线控制、Wi-Fi 加入 Setup AP 的
  双网口 Linux 客户端访问真实恢复页并透明转发 Device Hub TLS。没有该物理客户端时
  `recovery_pairing` 必须保持 BLOCKED，USB/Host seam 不得替代。
- 任何会修改 Companion Profile 的 Pairing 前，控制端必须先通过独立事务取得并保存
  原 Profile 快照；SSH 返回或客户端网络清理失败不能丢失补偿依据。
- Linux 客户端在切换 Wi-Fi 前只持久化非秘密 UUID 补偿日志；控制端每次事务后必须用
  新 SSH 连接执行幂等 cleanup/verify，并以跨进程事务锁等待旧 primary 完全退出。SSH
  cleanup 还必须留下 cancellation fence，使尚未取得锁的迟到 primary 永久拒绝变更。
  控制口必须由 NetworkManager 明确识别为 Ethernet。每次请求都重新绑定受验 helper
  SHA-256。

### M2：Codex 首页

跟踪规格：[#21](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/21)

- 官方 App Server 额度和 Token。
- abtop 风格只读会话观察。
- VERIFIED/INFERRED/UNAVAILABLE 契约。
- Codex 默认首页、动态额度窗口和离线快照。

### M3：其他 Provider

跟踪规格：[#22](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/22)

- Cursor 实验适配器。
- AIHubMix、DeepSeek 模板和通用 HTTP Provider。
- SQLite 90 天历史。
- OS 凭据存储和 `age` 加密备份。
- Provider 循环页面。

### M4：串口工具

跟踪规格：[#23](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/23)

- `DISARMED → USB TX ↔ WEB TX` 状态机。
- 受控 GPIO17 TX、UART Router、512 KiB PSRAM ring、USB bridge。
- Companion 8 MiB ring 和多浏览器观察。
- xterm.js + pineTERM 功能集成。
- 115200 长稳压力测试。

### M5：产品化

跟踪规格：[#24](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/24)

- 最多 5 个 Companion、优先级和粘性故障切换。
- 签名 OTA、A/B 回滚。
- 诊断包、安装、自启动和升级迁移。
- macOS/Windows 72 小时验收。

## 18. 测试与验收

### 18.1 自动化测试

- Provider DTO：缺字段、null、未知枚举、未知小版本、过大响应。
- Codex：官方额度与推测状态不能混淆。
- Cursor：私有 schema 变化时隔离失败并返回 unavailable。
- curl 导入只解析白名单字段，不能执行命令。
- JSONPath 映射、货币、时间和异常数值。
- WSS 配对、证书不匹配、Token 撤销、协议版本不兼容。
- 配置 write-through 和设备拒绝路径。
- `age` 正确密码、错误密码、损坏文件、合并冲突和事务回滚。
- NVS 双快照断电恢复和 CRC 错误。
- UART Router 各 sink 的独立背压和计数。
- USB/WEB Owner 切换、Lease 超时和退出清理。

### 18.2 硬件在环

- 115200 8N1 连续 72 小时，校验发送/接收/丢弃总量。
- 同时执行 Wi-Fi 重连、WSS 重连、页面切换、USB 插拔和 Web 客户端堵塞。
- 电脑不开 COM、COM 被占用、USB 断开再连接。
- Companion 休眠/重启、DHCP 地址变化和 Active Companion 故障切换。
- 进入串口页前确实无应用层 UART 桥；退出后立即停止。
- WEB TX 期间 USB 输入不进入目标 UART；回退后 USB 恢复。
- 测试 230400/460800/921600；只有通过同等级测试才标记 verified。
- Setup AP 密码、超时和恢复流程。
- OTA 下载中断、供电中断、签名错误和新固件健康检查失败。

### 18.3 跨平台验收

- macOS 13+ Apple Silicon 连续运行 72 小时。
- Windows 11 x64 连续运行 72 小时。
- 登录自启动、睡眠唤醒、网络切换和防火墙提示。
- Codex/Cursor 路径发现与无权限状态。
- 从 Mac 导出加密备份，在 Windows 导入 Provider 与 API Key。
- Windows 重新登录 Codex/Cursor并建立独立设备配对。

### 18.4 V1 发布门槛

- Codex/Cursor/自定义 Provider 单独失败不影响其他模块。
- 所有数值带时间戳；旧数据明确 stale，未知值不显示为零。
- 外部 Codex 会话不被标成 verified。
- 屏幕无串口正文；Web/USB 原始流不被 UI 转码破坏。
- 115200 长稳测试无 UART 主链路丢包；硬件溢出必须准确报警。
- API Key、Cookie、Prompt 和串口正文不出现在日志、AI 快照或 ESP32 持久存储。
- OTA 失败可回滚，BOOT/UART 可恢复。

## 19. 主要风险

| 风险 | 应对 |
| --- | --- |
| Cursor 个人接口变化 | 实验适配器、schema 校验、错误隔离、保留 unavailable 页 |
| 外部 Codex 会话无法精确观察 | 显式 INFERRED，状态集合受限，不显示假审批状态 |
| OpenUsage 等上游快速变化 | 固定提交、保留许可证、适配层测试、定期人工升级 |
| Go Companion 与 ESP 协议漂移 | 共享 schema/fixtures、major version gate |
| Windows 路径和权限差异 | 真实 Windows 11 验收，不只做单元测试 |
| WSS 本地证书轮换 | 每个 Profile 固定指纹，显式重新配对，不静默信任新证书 |
| 串口慢消费者 | 独立 sink ring、丢最旧、独立计数、UART RX 不阻塞 |
| USB/Web 同时发送 | 单 TX Owner、切换清队列、USB rejected counter |
| U0TXD 复位日志注入目标 | 目标 TX 改用 GPIO17；丝印 TXD 不连接目标 RX |
| Flash 磨损或快照损坏 | 30 分钟写入上限、双 blob、CRC、原子 active marker |
| 固件升级失败 | 签名、A/B、健康确认、回滚和 UART 恢复 |

## 20. 参考资料

### 板卡与固件

- [微雪 ESP32-S3-RLCD-4.2 文档](https://docs.waveshare.net/ESP32-S3-RLCD-4.2)
- [微雪 ESP32-S3-RLCD-4.2 GitHub](https://github.com/waveshareteam/ESP32-S3-RLCD-4.2)
- [ESP-IDF 6.0.2 ESP32-S3 Get Started](https://docs.espressif.com/projects/esp-idf/en/v6.0.2/esp32s3/get-started/index.html)
- [ESP-IDF USB Serial/JTAG](https://docs.espressif.com/projects/esp-idf/en/v6.0.2/esp32s3/api-guides/usb-serial-jtag-console.html)
- [ESP-IDF USB Device/TinyUSB](https://docs.espressif.com/projects/esp-idf/en/v6.0.2/esp32s3/api-reference/peripherals/usb_device.html)
- [LVGL 9.4](https://docs.lvgl.io/9.4/)

### Codex、Cursor 与采集参考

- [Codex App Server 官方文档](https://learn.chatgpt.com/docs/app-server)
- [Codex Pricing and Usage](https://learn.chatgpt.com/docs/pricing)
- [Cursor API 文档](https://cursor.com/docs/api)
- [OpenUsage](https://github.com/janekbaraniewski/openusage)
- [abtop](https://github.com/graykode/abtop)
- [Token Monitor](https://github.com/Javis603/token-monitor)
- [Tokscale](https://github.com/junhoyeo/tokscale)
- [TokenTracker](https://github.com/xiufengsun/TokenTracker)
- [本项目 AI 采集方案对比](research/ai-usage-collector-comparison.md)

### Web 串口

- [xterm.js](https://github.com/xtermjs/xterm.js)
- [pineTERM](https://github.com/WeSpeakEnglish/pineTERM)
- [GoogleChromeLabs Serial Terminal](https://github.com/GoogleChromeLabs/serial-terminal)

## 21. 下一步

M0 功能代码合并后立即搭建 M1 的 Go Companion、配对和 WSS 骨架；先用固定模拟快照打通 Codex 首页，再接入真实采集器。M1 实现可与 M0 剩余物理/长稳证据并行，但 V1 发布仍要求 M0 验收报告最终通过。这样可以尽早验证跨端协议和恢复路径，避免业务适配器掩盖基础问题。
