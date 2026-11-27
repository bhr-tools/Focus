// Package web holds the kit's browser files. Apps serve them under /kit/
// (see kit/server), so pages import /kit/css/base.css and /kit/js/*.js.
package web

import "embed"

//go:embed css js node sw.js
var Files embed.FS
