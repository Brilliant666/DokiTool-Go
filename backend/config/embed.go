// Package config embeds the existing language-neutral platform registry.
// platforms.json remains the sole source used by PHP, Go and frontend generation.
package config

import _ "embed"

//go:embed platforms.json
var platforms []byte

func Platforms() []byte { return append([]byte(nil), platforms...) }
