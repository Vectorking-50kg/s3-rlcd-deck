# S3 RLCD Deck

面向 Waveshare ESP32-S3-RLCD-4.2 的双用途桌面终端：

- 默认显示 Codex 会话、Token、额度窗口，以及 Cursor、AIHubMix、DeepSeek 等 Provider 的用量或余额。
- 长按 KEY 进入 UART 工具，通过 USB 和 Companion Web 控制台进行串口监控与双向桥接；开发板屏幕只显示状态和统计。

系统由 ESP32-S3 固件和跨平台 Go Companion 组成。Companion 负责 Mac/Windows 本机采集、AI API、完整 Web SPA、加密配置和实时串口终端；ESP32 负责 400×300 RLCD、按键、串口实时链路和离线显示。

## 文档

- [开发文档](docs/DEVELOPMENT.md)
- [AI 用量采集开源方案对比](docs/research/ai-usage-collector-comparison.md)

当前仓库已进入 M0 固件实现阶段；功能边界、验收标准和任务依赖以 GitHub Issues 为准。

## 开发

固件工程和可复现命令见 [firmware/README.md](firmware/README.md)。工程要求 ESP-IDF 6.0.2；开发构建提供 HIL 使用的结构化启动诊断，发布构建默认关闭该通道。

M0 发布门槛、证据哈希与当前阻塞项统一记录在
[M0 验收报告](docs/acceptance/m0.md)；只有自动 smoke/soak 和第二设备人工验收全部通过时，报告才会生成 `PASS` 结论。
