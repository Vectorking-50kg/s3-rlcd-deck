# S3 RLCD Deck — 仪表界面原型

> 仅供原型评审——此目录是可丢弃的视觉参考，不与固件或 Companion API 连接。

正式实现位于 `companion/web/dist/`，并通过 `GET /api/v1/console` 及现有管理接口连接真实数据。本目录继续保留为方案 C 的设计依据和状态覆盖参考。

方案 C（仪表面板）已被选为唯一方向。主框架由 5 个领域 Dock、当前领域的上下文侧栏和高密度检查画布组成；A/B 对比切换器已从入口移除。

## 运行

在仓库根目录执行：

```bash
python3 -m http.server 4173 --directory docs/ui-prototype
```

打开 <http://127.0.0.1:4173/>。不需要构建、安装依赖、外部字体、CDN、网络请求或真实数据写入。

## 完整界面清单

| 领域 | 界面 |
| --- | --- |
| 首页 | 概览 |
| AI 采集 | AI Provider、Provider 编辑器、用量历史、Codex 会话 |
| 串口工作台 | 实时终端、串口预设 |
| 设备 | Deck 清单、网络与信任、Setup / 恢复、Deck RLCD 模拟器 |
| 系统 | 系统设置、固件更新、备份与恢复、诊断、托盘 / 菜单、访问 / 登录 |

原型包含 17 个路由界面及其完整任务流程：

- Provider 请求、映射、脱敏预览和凭据边界。
- 90 天历史、CSV 导出、保留策略和清空确认。
- Web TX 租约获取与释放、Text/ANSI、HEX、混合视图、导出和预设。
- 多 Deck 在线 / 离线检查、设备自有直写设置、配对、Token 轮换和信任撤销。
- Setup Wi-Fi 候选验证、失败恢复、Companion Profile、设备设置和清除 Wi-Fi 确认。
- 签名 A/B OTA、健康确认、回滚证据和更新历史。
- 加密导出、导入预览、合并 / 替换 / 仅 Provider、冲突处理和安全失败状态。
- 健康状态、脱敏事件日志、隐私报告和支持包预览。
- 托盘授权、手动 Token、会话过期和本机限流状态。
- 13 个 400×300 RLCD 状态，包括无 Provider 提示、Companion 故障切换和串口统计。

## 状态与响应式评审

顶部“评审状态”控件可以为每个管理界面切换“默认”“加载中”“空状态”和“可恢复错误”。Setup、Deck、托盘、登录及工作流页面还提供各自的领域状态选择。

验证脚本覆盖 17 个路由在 1440×1000 与 375×812 下的表现、48 个异常状态、页面级横向溢出、JavaScript 错误和关键交互：

```bash
NODE_PATH=/path/to/node_modules node docs/ui-prototype/verify.cjs
```

设计规则与生产交接约束记录在 [DESIGN_SYSTEM.md](DESIGN_SYSTEM.md)。
