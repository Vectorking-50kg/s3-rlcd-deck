# Deck 低分辨率中文字体方案研究

> 调研日期：2026-08-19
> 目标平台：ESP32-S3、400×300 纯黑白 RLCD、LVGL 9.4.0
> 证据范围：项目锁定源码、成熟开源项目的官方文档、源码仓库和许可证；不采用博客或二手测评。
> 结论性质：字体设计与迁移建议；本文不修改生产代码。

## 1. 结论摘要

**继续使用 `lv_font_conv` 生成并编译进固件的 LVGL C 字体，保留 Source Han Sans SC，默认使用 1 bpp 和转换器默认的单色自动 hinting。** 这是当前约束下风险最低、Flash/RAM 最省、与现有 LVGL 布局和许可证边界最一致的主路线。

具体建议如下：

1. **当前直接执行 16/20/32 三档：16 px 用于紧凑中文正文，20 px 用于中文标题与关键状态，32 px 只用于 ASCII/数字 hero。** 12–14 px 不承载一般中文；18 px 作为后续实机 A/B 候选，不要求先改变已实现管线。
2. **不要为纯黑白面板改成 2/4 bpp 抗锯齿，也不要使用 LCD 子像素模式。** 当前链路最终把 RGB565 以固定阈值压成 1 bit；中间灰阶会被丢弃，而 A4 字体位图接近 A1 的四倍。
3. **16/20 px 保留同一 Source Han Sans SC 同尺寸内的中英文混排；32 px 保持 ASCII-only。** ASCII 和中文在同一档生成字体中共享 baseline，优于临时拼接不同字体。只有同源、同尺寸、经生成后验证同 metrics 的常用汉字包才适合作为 fallback。
4. **继续以字符清单做子集，但把清单升级为可验证的产品契约。** 静态中文 UI、协议状态词和允许出现的动态字符都必须自动并入清单；任意用户输入不能靠一个几十字的静态子集兜底。
5. **`binfont` 不是当前小字库的优化。** LVGL 9.4 的 loader 会把字形 bitmap、descriptor、cmap 和 kerning 整体分配到 RAM；它适合可下载/可切换字体包，不适合一个启动后一直使用的小核心字库。
6. **U8g2 的 WenQuanYi 点阵字可作为 12–16 px 的实机 A/B 基准或后备路线，不作为第一阶段替换。** 它在小字号上有真正的像素级设计优势，但只有 12–16 五档，接入 LVGL 需要 BDF 转换或自定义 font engine，且字体上游是 GPL v2 with font embedding exception，合规复杂度高于现有 OFL。
7. **不采用 Adafruit GFX fontconvert 或运行时 FreeType 作为主路径。** 前者的官方实现只处理 7/8-bit 连续字符范围，`write()` 仍以单字节字符索引工作；后者需要完整字体文件、引擎代码、heap 和 glyph cache。FreeType 应继续只在开发机的 `lv_font_conv` 内作为构建时光栅器。

## 2. 当前仓库基线

仓库已经具备一条合理的最小字体链路：

- LVGL 通过依赖锁固定为 9.4.0，组件记录的上游提交为 `c016f72d4c125098287be5e83c0f1abed4706ee5`；见[依赖锁](../../firmware/dependencies.lock)与本地 managed component 的 `idf_component.yml`。LVGL 本身使用 [MIT 许可证](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/LICENCE.txt)。
- [生成脚本](../../tools/generate_m0_font.sh)固定 `lv_font_conv@1.5.3`，使用 LVGL 依赖内的 `SourceHanSansSC-Normal.otf`，统一采用 1 bpp、ASCII `0x20–0x7e`、无 kerning；它已生成 **16/20 px CJK+ASCII** 和 **32 px ASCII-only** 三档。
- [字形清单](../../firmware/components/application_ui/assets/m0_glyphs.txt)当前有 **206 个唯一 symbols：203 个基本区汉字，加 `°`、`·` 与 `、`**。16/20 px 各包含这 206 个 symbols 与 95 个可打印 ASCII，共 301 个有效 glyph、另一个保留 descriptor；32 px 只有 95 个 ASCII glyph。
- [16 px 字体](../../firmware/components/application_ui/assets/lv_font_deck_m0_16.c)为 `line_height=19`、`base_line=5`、bitmap 6,154 B；[20 px 字体](../../firmware/components/application_ui/assets/lv_font_deck_ui_20.c)为 `line_height=23`、`base_line=6`、bitmap 9,571 B；[32 px ASCII 字体](../../firmware/components/application_ui/assets/lv_font_deck_ui_32.c)为 `line_height=37`、`base_line=9`、bitmap 3,574 B。
- ESP-IDF 6.0.2 的 Xtensa object 实测三档只读字体数据分别为 **9,090 B、12,507 B、4,426 B，合计 26,023 B**。这里未把 debug section 或 C 源码文本大小当成固件 Flash 成本；release 构建应持续用 object/map 文件校验。
- [renderer](../../firmware/components/application_ui/deck_ui_renderer.cpp)让中文 hero 使用 20 px，而只有 `hero_is_code` 时使用 32 px；因此 32 px 不含中文是一个有意的资源边界，不是缺字遗漏。
- Source Han Sans SC 使用 [SIL Open Font License 1.1](https://github.com/adobe-fonts/source-han-sans/blob/release/LICENSE.txt)；仓库已保留[对应许可证副本](../../firmware/components/application_ui/assets/SourceHanSansSC-OFL.txt)和 [NOTICE](../../firmware/NOTICE.md)。

显示端的关键事实是：[打包实现](../../firmware/components/display/deck_display_service.cpp)将 LVGL 的 RGB565 像素以 `pixel >= 0x7fff` 判为白色，否则判为黑色。对于当前“白底黑字”中性色 UI，这相当于把抗锯齿覆盖率再次阈值化，而不是保留灰阶。

## 3. 1 bpp、hinting 与抗锯齿

### 3.1 为什么中间使用 4 bpp 不会让最终 RLCD 更清晰

LVGL 9.4 把普通字体像素解释为 opacity；官方说明 A4 数据接近 A1 的四倍，高 bpp 的作用是用更多透明度等级平滑边缘。[LVGL 9.4 字体格式](https://docs.lvgl.io/9.4/details/main-modules/font.html#font)  FreeType 也明确说明：除 mono 外的 render mode 产生 256 级 coverage，而 mono 是 1-bit bitmap。[FreeType glyph retrieval / render modes](https://freetype.org/freetype2/docs/reference/ft2-glyph_retrieval.html#ft_render_mode)

在本项目中，A2/A4 字形先混合为 RGB565 灰边，再被显示服务压成黑/白。由此可以推断：

- 灰边不会抵达物理面板；
- 最终形状取决于阈值，可能与 FreeType 专门的 mono rasterizer 不同；
- A4 bitmap 约四倍的 Flash 与额外 A8 draw-buffer 转换没有换来物理灰阶；
- `--lcd`/`--lcd-v` 生成三倍横向/纵向子像素 bitmap，但 RLCD 没有 RGB 子像素通道，不能保留该信息。[lv_font_conv 1.5.3 参数](https://github.com/lvgl/lv_font_conv/blob/899ea1128d2e82bb015a319c8a7d18a82359ab3a/README.md#cli-params)

因此，纯黑白 UI 应直接在生成阶段得到最终 1-bit 轮廓，而不是生成抗锯齿后再阈值化。也不建议用抖动模拟灰色边缘：它会把稳定笔画变成空间噪点，并增加 RLCD 局部刷新后的视觉不确定性。

### 3.2 当前 1 bpp 实际启用了什么

`lv_font_conv@1.5.3` 在 `bpp === 1` 且未启用 LCD 子像素时设置 `mono`；随后使用 `FT_LOAD_TARGET_MONO`，默认再加 `FT_LOAD_FORCE_AUTOHINT`。[转换器 glyph 采集](https://github.com/lvgl/lv_font_conv/blob/899ea1128d2e82bb015a319c8a7d18a82359ab3a/lib/collect_font_data.js#L88-L116) [转换器 FreeType flags](https://github.com/lvgl/lv_font_conv/blob/899ea1128d2e82bb015a319c8a7d18a82359ab3a/lib/freetype/index.js#L164-L205)

FreeType 官方把 `FT_LOAD_TARGET_MONO` 定义为只应在单色输出使用的强 hinting，并提醒自动生成的小字号结果仍取决于字体和 hinter；没有适合黑白输出的 hint 时，小字号可能难看。[FreeType load target 说明](https://freetype.org/freetype2/docs/reference/ft2-glyph_retrieval.html#ft_load_target_xxx)

对当前脚本的含义是：

- **保留默认自动 hinting。** `--autohint-off` 会禁止 auto-hinter，应只作为实机 A/B 变量；没有实板证据时不应改变这项稳定基线。
- **不要增加 `--autohint-strong`。** 1 bpp 已先进入 mono 分支，strong/light 的选择只在非 mono 分支执行；该参数对当前目标不是正确调节杆。
- **继续显式写出 `--no-compress --no-prefilter`。** 1.5.3 源码对 1 bpp 本来就返回 uncompressed bitmap format；这些参数使意图清楚，不会损失一个实际可用的压缩收益。[1.5.3 compression code](https://github.com/lvgl/lv_font_conv/blob/899ea1128d2e82bb015a319c8a7d18a82359ab3a/lib/font/table_glyf.js#L169-L180)

LVGL 的通用文档指出压缩字体渲染约慢 30%，且越大、bpp 越高越有效；这一建议主要适用于大号多灰阶字体，不适用于当前 1 bpp 小子集。[LVGL compressed fonts](https://docs.lvgl.io/9.4/details/main-modules/font.html#compressed-fonts)

## 4. 12–20 px 的可读性与成本

使用当时的 **202-symbol manifest**、同一个 OTF 和 `lv_font_conv@1.5.3`，只改变 `--size`，本次得到以下字号对照；当前生产清单已随新增页面扩展到 206 symbols：

| `--size` | `line_height` | `base_line` | bitmap bytes | 估算静态字体数据 | 建议用途 |
|---:|---:|---:|---:|---:|---|
| 12 | 15 | 4 | 3,650 | 约 6,546 B | 数字、ASCII 缩写；避免一般中文正文 |
| 14 | 16 | 4 | 4,779 | 约 7,675 B | 低笔画次要标签；高密度汉字需逐字验证 |
| 16 | 19 | 5 | 6,154 | **9,090 B（当前 object 实测）** | 中文紧凑正文的最低默认值 |
| 18 | 21 | 5 | 7,493 | 约 10,389 B | 后续正文清晰度 A/B 候选 |
| 20 | 23 | 6 | 9,571 | **12,507 B（当前 object 实测）** | 中文标题、告警、短状态 |

当前 16/20 px object 包含 302 个 descriptor、两个 cmap、206 项 Unicode list 和 font descriptor；bitmap、`line_height` 与 `base_line` 直接来自对应生成物，并由当前 Xtensa object 精确复核。这里最重要的不是每档相差一两 KiB，而是 12–14 px 留给复杂汉字的有效像素网格太小。

建议建立一组“困难字”硬件样张，至少覆盖：

- 密集横竖和封闭空间：`断、湿、最、器、警`；
- 相近结构：`未/末、土/士、己/已`；
- 中英数字混排：`Codex 会话 12`、`Wi‑Fi 配对`、`USB 状态`；
- 标点与负号：中文全角标点、ASCII 冒号、百分号、温度单位。

验收应在实机 RLCD 上进行，不用桌面放大的 PNG 替代。先验收当前 16/20 px；如果 16 px 正文不够清楚，再比较 16/18 px、正常与反白、一次全刷与多次局刷后的可辨识度。

## 5. 方案逐项比较

| 方案 | 12–20 px 中文 | Flash / RAM | LVGL 与混排 | 许可证与复现 | 结论 |
|---|---|---|---|---|---|
| LVGL C 字体 + Source Han + `lv_font_conv` | 当前 16/20 CJK；32 ASCII；12/14 风险高 | 当前三档合计约 25.1 KiB Flash；无整库字体 heap/FS | 原生；同源 ASCII+CJK baseline 最稳 | LVGL/转换器 MIT，字体 OFL；当前链路已固定版本 | **主路线** |
| LVGL `binfont` | 与同参数 C 字体完全相同 | 文件在存储中，但 loader 把 bitmap、descriptor、cmap、kern 分配到 RAM | 原生；需要 LVGL filesystem 和失败处理 | 格式由同一转换器生成；可做可下载包 | 仅在动态字体包阶段采用 |
| U8g2 + WenQuanYi 点阵 | 12–16 有像素级设计优势；没有 18/20 原生档 | full GB2312 为约 198–301 KiB；小清单可大幅缩小 | 非 LVGL 字体格式；需 BDF→TTF→LVGL 或自定义 engine | U8g2 BSD-2-Clause；WQY 为 GPL v2 + font embedding exception | **实机基准/备选** |
| Adafruit GFX `fontconvert` | 单色清晰，但官方链路不支持 UTF‑8 CJK | 1 bpp、紧凑；bitmap offset 16 bit，约 64 KiB 上限 | 不兼容 LVGL；官方 `write(uint8_t)` 是单字节索引 | BSD；构建依赖系统 FreeType | 排除 |
| LVGL 运行时 FreeType | 可任意字号和字符，但最终仍需 mono 策略 | 引擎代码 + 完整字体文件 + heap/cache；当前 OTF 为 16,414,944 B | LVGL 扩展；9.4 文档注明当前不支持 kerning | FreeType 可选 FTL 或 GPLv2；字体仍是 OFL | 静态 UI 排除 |

### 5.1 LVGL C 字体：最适合当前产品

LVGL 官方把 offline `lv_font_conv` 列为标准加字方式，并支持按 range/symbol 子集、1–4 bpp、kerning 和 C 输出。[LVGL adding a font](https://docs.lvgl.io/9.4/details/main-modules/font.html#adding-a-new-font) [lv_font_conv 1.5.3](https://github.com/lvgl/lv_font_conv/tree/899ea1128d2e82bb015a319c8a7d18a82359ab3a)

它的核心优势不是“转换方便”，而是生成的 bitmap、descriptor 和 cmap 都是只读常量，可由 ESP32 从 Flash 映射；显示时只使用 LVGL 已有的字形 draw buffer。对一个始终存在的核心中文 UI，这比运行时 loader 和字体引擎更深、更简单。

当前的 `--no-kerning` 可以保留：中文全角 glyph 不依赖成对 kerning，少量 ASCII 专用名词的视觉收益不足以抵消宽度回归和 kerning 表。若未来面向长篇英文正文，再单独评估，不要与中文清晰度改动绑在一起。

### 5.2 LVGL binfont：省构建链接，不省运行时 RAM

官方说明 `--format bin` 产生 LVGL 专用二进制文件，并通过 `lv_binfont_create()` 从 LVGL filesystem 加载。[LVGL runtime font loading](https://docs.lvgl.io/9.4/details/main-modules/font.html#loading-a-font-at-run-time)

LVGL 9.4.0 的 loader 源码会为 `lv_font_t`、format descriptor、cmaps、Unicode lists、glyph descriptors、**全部 glyph bitmap** 和 kerning 分别 `lv_malloc`，销毁时逐项释放。[LVGL 9.4.0 binfont loader](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/src/font/lv_binfont_loader.c#L91-L170) [glyph load](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/src/font/lv_binfont_loader.c#L330-L445)

所以 binfont 的合理触发条件应是“字体包需要 OTA 下载、切换或卸载”，而不是“希望节省当前约 25.1 KiB 三档字体”。若以后采用，必须把 loader 峰值 heap、filesystem 失败、校验和、版本兼容与回退字体纳入验收。

### 5.3 U8g2 CJK 点阵：清晰度参考价值高，直接接入成本高

U8g2 官方维护的 WenQuanYi 组来自手工点阵 BDF，提供 12×12、13×13、14×14、15×15、16×16 五档，并明确说明其目标是避免低分辨率上矢量 CJK 因 hinting 不足造成的模糊。[U8g2 WenQuanYi 字体说明](https://github.com/olikraus/u8g2/wiki/fntgrpwqy) 这与 LVGL 9.4 官方建议一致：小型低分辨率屏可用 BDF bitmap，并经 `mkttf` 转成带 embedded bitmap 的 TTF 后再交给 `lv_font_conv`。[LVGL using a BDF font](https://docs.lvgl.io/9.4/details/main-modules/font.html#using-a-bdf-font)

但不应直接引入 U8g2 的 full GB2312 数组。当前官方源码中各档生成数组的实际大小如下；`chinese1` 含 413 个 glyph（包括 ASCII），`gb2312` 含 7,541 个 glyph（包括 ASCII）。这些是 U8g2 自身压缩格式与索引的大小，不能直接当作 LVGL 生成物大小，但足以反映子集的重要性。[U8g2 当前 WQY 数组](https://github.com/olikraus/u8g2/blob/ab9e48b2228351e9476682a70b7f3ee4909cd585/csrc/u8g2_fonts.c#L143497-L227723)

| WQY 点阵档位 | `chinese1` | `gb2312` |
|---:|---:|---:|
| 12 px | 9,391 B | 202,690 B |
| 13 px | 10,187 B | 224,825 B |
| 14 px | 11,142 B | 247,515 B |
| 15 px | 12,246 B | 270,959 B |
| 16 px | 13,882 B | 308,472 B |

U8g2 官方也建议按实际字符生成 custom font，而不是使用 full font。[U8g2 Flash/font optimization](https://github.com/olikraus/u8g2/wiki/u8g2optimization#font-optimization)

接入有两条路：

1. **实验首选：** WQY BDF → 固定版本 `mkttf`/FontForge/potrace 工具链 → `lv_font_conv --bpp 1 --format lvgl`，继续使用 LVGL 原生 font；
2. **不建议起步：** 为 U8g2 压缩格式写 `lv_font_t` adapter，负责 Unicode lookup、bitmap 解码、metrics 和 baseline。

许可证必须按组成部分处理：U8g2 代码是 [BSD-2-Clause](https://github.com/olikraus/u8g2/blob/ab9e48b2228351e9476682a70b7f3ee4909cd585/LICENSE)，但官方 WQY 页面和 BDF 元数据标明字体为 **GPL v2 with font embedding exception**。`u8g2_wqy` 包装仓库的 MIT 许可证不能覆盖或消除上游字体条款。[WQY 上游包装仓库](https://github.com/larryli/u8g2_wqy/tree/34d1bf7e054ef5a331c45248cd238edc49498897)

因此，WQY 适合作为 12–16 px 清晰度 benchmark；只有当当前 Source Han 16 px 实机仍不达标、18 px A/B 也不能解决问题，且法务/NOTICE 与转换工具链都明确后，才升级为生产候选。

### 5.4 Adafruit GFX / fontconvert：技术方向相似，但字符模型不适合中文

Adafruit 的官方 `fontconvert` 也用 FreeType `FT_LOAD_TARGET_MONO` 和 `FT_RENDER_MODE_MONO` 生成紧凑 1 bpp bitmap，并记录 baseline、offset 和 advance；这证明“构建时单色光栅化”是成熟 MCU 路线。[Adafruit fontconvert](https://github.com/adafruit/Adafruit-GFX-Library/blob/ac6d7c3869a693d406f77b9bfcd486b0673169f0/fontconvert/fontconvert.c#L119-L201)

但该工具源码明确写着“currently only extracts printable 7-bit ASCII”；它按 `first..last` 连续处理字符。GFX 的实际输出路径是 `write(uint8_t c)`，运行时把 `first/last` 读成 8 bit 后索引 glyph，且 bitmap offset 只有 16 bit、工具注释约 64 KiB 上限。[GFX font structs](https://github.com/adafruit/Adafruit-GFX-Library/blob/ac6d7c3869a693d406f77b9bfcd486b0673169f0/gfxfont.h) [GFX write path](https://github.com/adafruit/Adafruit-GFX-Library/blob/ac6d7c3869a693d406f77b9bfcd486b0673169f0/Adafruit_GFX.cpp#L1499-L1538)

要支持中文必须 fork 字符解码、稀疏 cmap、descriptor lookup 和 converter，还要绕开现有 LVGL。其 [BSD 许可证](https://github.com/adafruit/Adafruit-GFX-Library/blob/ac6d7c3869a693d406f77b9bfcd486b0673169f0/license.txt) 很宽松，但技术迁移没有收益，故排除。

### 5.5 运行时 FreeType：能力过剩，资源模型不合适

LVGL 9.4 的 FreeType extension 可以运行时从 vector/bitmap font 生成 glyph，嵌入式配置会裁掉不常用模块；但仍需 FreeType 库、字体文件、运行时分配和缓存，且 LVGL 文档注明 FreeType 集成当前不支持 kerning。[LVGL FreeType engine](https://docs.lvgl.io/9.4/details/libs/freetype.html) [LVGL kerning comparison](https://docs.lvgl.io/9.4/details/main-modules/font.html#kerning)

9.4.0 配置模板默认缓存 **256 个 glyph**，并明确提示数量越高越耗内存；启用 FreeType 且使用 LVGL OS 绘图线程时，内部配置检查要求 draw-thread stack 至少 **32 KiB**。字形 cache miss 后，渲染结果还会复制进由缓存持有的 LVGL draw buffer。[FreeType cache 配置](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/lv_conf_template.h#L973-L982) [draw-thread stack 约束](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/src/lv_conf_internal.h#L4809-L4814) [glyph image cache 路径](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/src/libs/freetype/lv_freetype_image.c#L128-L215)

当前 Source Han OTF 是 16.4 MiB，而当前 16/20 CJK + 32 ASCII 三档的实测只读数据合计约 25.1 KiB。即使后续先对子集 OTF，运行时方案仍比离线光栅化多出引擎、heap、缓存失效和文件 I/O。FreeType 的软件可选择 [FreeType License 或 GPL v2](https://freetype.org/license.html)，采用 FTL 时还需在产品文档中明确致谢。

## 6. 中文主体 UI 的字形与混排规则

### 6.1 静态中文和专用名词

- 用户可见 UI 默认中文；`Codex`、`Wi‑Fi`、`USB`、`UART`、`LVGL`、`ESP32-S3` 等专用名词保留原文。
- 同一个 label 内使用当前 Source Han 字体的 ASCII 和 CJK，不为“更漂亮的英文”临时接另一个字体。
- CJK 使用 16 px 整数 advance；ASCII 保持比例宽度。不要通过 letter spacing 强行把所有 ASCII 变成等宽，串口/日志确有对齐需求时应使用独立 ASCII 字体和独立组件。
- 中文全角标点必须显式进入 manifest；不能假定 `0x20–0x7e` 会覆盖 `，。！？：`。

### 6.2 baseline 与 fallback

LVGL 的 `fallback` 会递归查找缺失 glyph，[官方文档](https://docs.lvgl.io/9.4/details/main-modules/font.html#fallback)与 [9.4.0 源码](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/src/font/lv_font.c#L96-L136)都支持这一点。但 label 的行高和绘制 baseline 仍以样式中的主字体为准，而 fallback 只提供 glyph descriptor/bitmap。[LVGL label draw](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/src/draw/lv_draw_label.c#L241-L287) [glyph placement](https://github.com/lvgl/lvgl/blob/c016f72d4c125098287be5e83c0f1abed4706ee5/src/draw/lv_draw_label.c#L580-L629)

因此 fallback 必须满足：

- 同一个 Source Han 字体文件；
- 同一个 `--size`、`--bpp`、hinting 参数；
- 相同 `line_height` 和 `base_line`；
- fallback 只放主字体缺少的 CJK，不重复 ASCII。

这些是**生成后需要验证的条件**，不是“同源同尺寸”自动提供的保证：独立子集包含的最高/最低 glyph box 不同，也可能得到不同的 `line_height`/`base_line`。构建时必须比较生成值，必要时在可复现的后处理里统一 metrics，并用上沿/下沿极端字形做裁切测试。

不同字体家族的 fallback 即使“字号都叫 16 px”，也可能因为 ascender/descender 和 glyph box 不同而上浮、下沉或被行框裁切。若必须混用，优先在 `lv_font_conv` 一次 merge 后检查生成 metrics；转换器只共享 baseline，不会自动缩放不同来源的 glyph。[lv_font_conv merged metrics](https://github.com/lvgl/lv_font_conv/blob/899ea1128d2e82bb015a319c8a7d18a82359ab3a/README.md#merged-font-metrics)

### 6.3 动态中文的产品边界

当前 203 汉字 manifest 已覆盖固定 UI 文案，但仍不能保证任意会话名、设备名或远端内容。产品必须选择并记录一种策略：

1. **受限动态文本：** 协议只允许 manifest 内字符和 ASCII；接收时做 Unicode code-point 校验，并显示明确替代文案；
2. **常用汉字 fallback：** 编译一个同源同尺寸的 3,000–7,000 字 fallback。按当前 16 px bitmap 与 descriptor 粗估为数百 KiB Flash、近零常驻 RAM，应由分区预算决定；
3. **可下载语言包：** 使用校验过的 binfont，但接受整包 RAM 和 filesystem 成本；
4. **任意 Unicode：** 需要流式外部 bitmap font engine 或运行时 FreeType，属于新的架构目标，不应伪装成“多加几个 glyph”。

对近期中文主体 UI，建议先采用 1；只有需求明确要求任意中文名称时再评估 2。缺字方框不能作为正式降级策略。

## 7. 推荐迁移路径

### 阶段 A：稳住现有 16 px 基线

1. 保持 Source Han Sans SC、1 bpp、默认 auto-hint、无 kerning、C 输出。
2. 把所有静态中文文案、全角标点和协议状态词汇总到一个规范 UTF‑8 清单；生成前去重并按 code point 排序。
3. 明确动态字段是否允许中文；每个字段分别定义 allowlist 或 fallback 文案。
4. CI 在临时目录重新生成字体并与 checked-in C byte-compare；检查每个 UI code point 都存在，并设置 compiled rodata 预算。
5. 在 Deck 实机保存 16 px 困难字样张，作为后续升级的对照。

### 阶段 B：固化已经实现的 16/20/32 分工

1. 保留 16 px 作为密集正文/表格，20 px 作为中文标题、中文 hero 和短告警，32 px 只作为 ASCII/数字 code hero；不把中文补进 32 px。
2. 12/14 px 组件不得承载未经审查的中文正文；若 16 px 实机不够清楚，再生成 18 px 与 16 px 做同文案 A/B，而不是预先替换当前 20 px 标题档。
3. 目前 16/20 px 共用同一 206-symbol manifest，简单且合计约 21.1 KiB；若以后需要缩减 Flash，再从实际调用点自动生成独立 manifest，不能手工维护两份易漂移清单。
4. 布局测试使用生成字体的真实 `adv_w`、`line_height`、`base_line`，并覆盖 renderer 中 16/20/32 的实际 label 高度；不要用“一个汉字约等于字号”的估算代替。

当前管线已经有两个正确的保护：host test 会检查两个生产文案源中的非 ASCII 字符都在 manifest 中，scene test 也覆盖配对码的 `hero_is_code`。但距离可发布的字体契约仍有以下差距：

- coverage test 只证明“文案字符在 manifest”，还没有证明 checked-in 的 **16/20 字体 C 文件**与 manifest 同步；CI 应重新生成后 byte-compare，或直接解析 cmap 验证每个 code point；
- 现有宽度测试只读取 16 px 的 ASCII advance；还需覆盖 20 px 中文标题、32 px code hero、renderer 的真实 label 高度，以及 32 px 路径只收到 ASCII 的不变量；
- 生成脚本直接在线运行 `npx --yes`，虽固定版本但还没有 lockfile/tarball cache、输入/输出 hash、NFC/排序/去重检查；
- 还没有针对三档字体的 object/map Flash 自动预算门；本文记录的 26,023 B 是当前 ESP-IDF 6.0.2 Xtensa object 实测值，后续 release 构建应持续验证；
- 自动测试不能替代 400×300 RLCD 实机样张；16/20 正反白、困难字以及重复局刷后的可辨识度仍需成为硬件验收项。

### 阶段 C：只有实机不达标才评估 WQY BDF

1. 只选当前 UI glyph，生成 WQY 14/16 与 Source Han 16/18 的同文案样张；现有 Source Han 20 px 同时作为大字号对照。
2. 先走 BDF→TTF→`lv_font_conv`，不引入 U8g2 runtime。
3. 固定 mkttf/FontForge/potrace 容器版本，保存源 BDF hash、映射清单、命令行和输出 hash。
4. 在确定视觉收益明显、18/20 px 缺口可接受、GPL font exception 的 NOTICE/分发义务明确后，才考虑替换。

### 阶段 D：动态字体包是独立功能

只有在语言包 OTA、用户可选字体或大规模动态中文成为明确需求后，才设计 binfont 或外部字体引擎。此阶段需要单独的安全与资源 ADR，包括：字体包签名、版本、最大文件和 glyph 数、heap 上限、加载超时、损坏回退、许可证随包分发。

## 8. 构建可复现性清单

当前脚本已经固定 `lv_font_conv@1.5.3`，该 npm release 对应官方 tag commit [`899ea1128d2e82bb015a319c8a7d18a82359ab3`](https://github.com/lvgl/lv_font_conv/tree/899ea1128d2e82bb015a319c8a7d18a82359ab3a)，并内置基于 FreeType 2.11.1 的 WebAssembly helper。[构建镜像定义](https://github.com/lvgl/lv_font_conv/blob/899ea1128d2e82bb015a319c8a7d18a82359ab3a/support/Dockerfile) 仍建议补齐以下约束：

- release 构建不直接依赖在线 `npx --yes`；改为项目 devDependency + lockfile + `npm ci`，或缓存 npm tarball；
- 记录 `lv_font_conv-1.5.3.tgz` 的 npm integrity：`sha512-0xJQThBOw2iptFccSXrKDIUTQAwr/2zhKjCI1lATIRgZo8uvYRTmenKafW9yTw6G0y5AyW00tqGpUtYuTuBIbQ==`；
- 继续依赖 ESP-IDF component lock 中的 LVGL component hash，避免 Source Han OTF 随 `managed_components` 漂移；
- 对 OTF、manifest、生成 C 分别保存 SHA-256，并在 CI 报告；
- manifest 必须是 NFC、无 CR、无重复 code point、排序稳定；
- 生成命令必须显式包含 size、bpp、hinting、compression、kerning、range、font name 和 include path；
- 生成后同时跑 glyph coverage、真实行宽/行高、固件 rodata 预算和实机黄金样张验收。

`--format dump` 可以输出逐 glyph PNG 与 JSON，适合 CI 制作差异图；但最终放行仍以 1:1 Deck 实机为准。[lv_font_conv dump format](https://github.com/lvgl/lv_font_conv/blob/899ea1128d2e82bb015a319c8a7d18a82359ab3a/README.md#cli-params)

## 9. 最终决策

近期 UI 开发应采用以下字体规范：

- **核心字体：** Source Han Sans SC，LVGL C，1 bpp，默认 mono auto-hint；
- **字号：** 16 px 紧凑中文正文，20 px 中文标题/关键状态，32 px ASCII/数字 hero；18 px 只作后续正文 A/B；12/14 px 限定用途；
- **字符：** ASCII + 自动汇总的中文静态 UI + 明确动态 allowlist；
- **混排：** 同源同尺寸；fallback 只允许同源同 metrics；
- **资源：** 字体常量驻 Flash，不为小核心字库启用 binfont/FreeType；
- **质量门：** code-point coverage、生成确定性、Flash 预算、混排 baseline、实机困难字样张；
- **备选：** WQY BDF 仅在 Source Han 实机失败后进行受控 A/B，不先引入 U8g2 runtime。

这条路线直接匹配当前 16/20 CJK + 32 ASCII 实现，三档合计约 25.4 KiB 只读数据；同时为 18 px 清晰度 A/B、按用途缩减 20 px manifest 和未来常用汉字 fallback 留出了清晰的演进边界。
