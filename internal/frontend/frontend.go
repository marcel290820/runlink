// Package frontend holds A's immutable public assets and their fixed routes.
package frontend

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"regexp"
)

var (
	//go:embed assets/capture.js
	capture []byte
	//go:embed assets/index.html
	index []byte
	//go:embed assets/loader.mjs
	loader []byte
	//go:embed assets/style.css
	style []byte
	//go:embed assets/manifest.json
	manifest []byte
)

var taskPath = regexp.MustCompile(`^/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// CSP admits only the inline capture script and the loader; trimming matches scripts/assets.py.
var CSP = "default-src 'none'; " +
	"script-src 'sha256-" + hash(bytes.TrimRight(capture, "\n")) + "' 'sha256-" + hash(loader) + "' 'strict-dynamic'; " +
	"style-src 'self'; connect-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; worker-src 'none'; " +
	"require-trusted-types-for 'script'; trusted-types runlink-gui"

func hash(data []byte) string {
	value := sha256.Sum256(data)
	return base64.StdEncoding.EncodeToString(value[:])
}

// Asset returns the public file for a fixed path. Every other path stays closed.
func Asset(path string) (data []byte, contentType string, ok bool) {
	switch {
	case path == "/" || taskPath.MatchString(path):
		return index, "text/html; charset=utf-8", true
	case path == "/assets/loader.mjs":
		return loader, "text/javascript; charset=utf-8", true
	case path == "/assets/style.css":
		return style, "text/css; charset=utf-8", true
	case path == "/assets/manifest.json":
		return manifest, "application/json", true
	}
	return nil, "", false
}
