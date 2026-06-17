// Package webfs embeds the built React UI for release builds. During
// development the Vite dev server serves the UI and proxies /api to the daemon;
// for release, this package's embedded files are served by the daemon.
//
// The embed directive requires web/dist to exist at build time. A placeholder
// dist/.gitkeep ensures the directory is present even before the first build.
package webfs

import "embed"

// Dist holds the built static assets under web/dist.
//
//go:embed all:dist
var Dist embed.FS
