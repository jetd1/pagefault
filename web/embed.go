// Package web embeds pagefault's static landing site so the
// binary can serve it at the root of the HTTP server without
// external assets on disk. Every file in this directory is
// governed by docs/design.md; edit the doc first if you change
// anything user-visible.
//
// The [Files] FS is consumed by internal/server, which mounts
// index.html at `/` and each named asset at its own path via
// [net/http.FileServerFS].
package web

import "embed"

//go:embed index.html styles.css script.js favicon.svg icons.svg icon.svg
var Files embed.FS
