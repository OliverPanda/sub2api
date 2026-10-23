package static

import "embed"

// Files contains the small, dependency-free recharge UI served by the bridge.
//
//go:embed index.html app.css app.js
var Files embed.FS

//go:embed index.html
var Index []byte
