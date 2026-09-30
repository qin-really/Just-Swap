// Package webui holds the browser UI as embedded static assets, so the app
// ships as one executable with nothing to deploy alongside it.
//
// Adding a file means adding it to the //go:embed line below. A file that is
// forgotten there is a build error, which is the point.
package webui

import "embed"

//go:embed index.html qrcode.min.js
var FS embed.FS
