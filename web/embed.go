// Package web embeds the Monitor.uz panel (HTML, JS, CSS, fonts, texts).
// Nothing is loaded from outside the hub.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html static i18n
var files embed.FS

// Files returns the panel file tree.
func Files() (fs.FS, error) { return fs.Sub(files, ".") }
