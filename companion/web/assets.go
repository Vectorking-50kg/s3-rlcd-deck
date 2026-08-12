package webapp

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist/index.html dist/app.css dist/app.js
var embeddedFiles embed.FS

func Handler() http.Handler {
	assets, err := fs.Sub(embeddedFiles, "dist")
	if err != nil {
		panic("embedded Web application is unavailable: " + err.Error())
	}
	return http.FileServer(http.FS(assets))
}
