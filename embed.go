// Package entregaviva embeds the static frontend so the server ships as a single binary.
package entregaviva

import (
	"embed"
	"io/fs"
)

//go:embed public
var public embed.FS

// Public returns the frontend files rooted at public/.
func Public() fs.FS {
	sub, err := fs.Sub(public, "public")
	if err != nil {
		panic(err) // the embed directive guarantees the directory exists
	}
	return sub
}
