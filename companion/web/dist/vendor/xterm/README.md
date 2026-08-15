# Embedded terminal assets

The Companion embeds these offline browser assets without a CDN:

- `@xterm/xterm` 6.0.0 (`xterm.js`, `xterm.css`)
- `@xterm/addon-fit` 0.11.0
- `@xterm/addon-search` 0.16.0
- `@xterm/addon-unicode11` 0.9.0

All four packages are MIT licensed. Their license texts are retained under
`companion/notices/licenses/` and are exposed by the Companion's existing
third-party notice route.

The HEX input, dual Text/HEX presentation, bounded log export, and structured
preset interaction adapt the design of MIT-licensed pineTERM at commit
`4860ae11f1dbb9a87626095847f6451fc70e26cc`. Its Web Serial transport and DOM
implementation are not used; this project keeps the existing authenticated
Companion WebSocket and Web TX Lease boundaries.
