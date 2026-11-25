// Package web embeds the browser UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed ui
var files embed.FS

// FS returns the UI file system with index.html at its root.
func FS() fs.FS {
	sub, err := fs.Sub(files, "ui")
	if err != nil {
		panic(err)
	}
	return sub
}
