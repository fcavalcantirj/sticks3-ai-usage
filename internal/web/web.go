package web

import (
	"embed"
	"strings"
)

//go:embed index.html base.css views.css core.js settings.js device.js render.js app.js
var assets embed.FS

// IndexHTML remains one self-contained document, so the Go daemon needs no
// asset routes, frontend build step, package manager, or runtime dependencies.
var IndexHTML = assemble()

func assemble() []byte {
	read := func(name string) string {
		b, err := assets.ReadFile(name)
		if err != nil {
			panic(err)
		}
		return string(b)
	}
	styles := read("base.css") + "\n" + read("views.css")
	var scripts []string
	for _, name := range []string{"core.js", "settings.js", "device.js", "render.js", "app.js"} {
		scripts = append(scripts, read(name))
	}
	html := strings.Replace(read("index.html"), "/* {{styles}} */", styles, 1)
	html = strings.Replace(html, "/* {{scripts}} */", strings.Join(scripts, "\n"), 1)
	return []byte(html)
}
