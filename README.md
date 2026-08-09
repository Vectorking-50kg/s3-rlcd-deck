# S3 RLCD Deck

面向 Waveshare ESP32-S3-RLCD-4.2 的双用途桌面终端：

- 默认显示 Codex 会话、Token、额度窗口，以及 Cursor、AIHubMix、DeepSeek 等 Provider 的用量或余额。
- 长按 KEY 进入 UART 工具，通过 USB 和 Companion Web 控制台进行串口监控与双向桥接；开发板屏幕只显示状态和统计。

系统由 ESP32-S3 固件和跨平台 Go Companion 组成。Companion 负责 Mac/Windows 本机采集、AI API、完整 Web SPA、加密配置和实时串口终端；ESP32 负责 400×300 RLCD、按键、串口实时链路和离线显示。

## 文档

- [开发文档](docs/DEVELOPMENT.md)
- [AI 用量采集开源方案对比](docs/research/ai-usage-collector-comparison.md)

当前仓库处于设计基线阶段，功能代码尚未开始实现。
