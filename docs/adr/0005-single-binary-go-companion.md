# Ship the Companion as a Go single binary with an embedded Web application

The Companion owns Provider collection, local persistence, device coordination, and the full management Web, and must run on macOS and Windows without a heavyweight desktop runtime. We will implement it as a Go background program that embeds the compiled SPA and exposes only thin platform adapters for menu-bar/tray and login-start integration; Electron and a separately deployed Web server were rejected because they increase packaging, update, resource, and trust-surface complexity.
