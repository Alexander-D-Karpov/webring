package webring

import (
	"embed"
)

//go:embed static internal/dashboard/templates internal/public/templates internal/user/templates
var Files embed.FS
