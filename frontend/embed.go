package frontend

import "embed"

//go:embed *.html *.js *.css
var Files embed.FS
