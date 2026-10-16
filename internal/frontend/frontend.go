// Package frontend embeds A's immutable public assets. scripts/assets.py
// generates index.html, loader.mjs, and manifest.json from the sources here.
package frontend

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"regexp"
)

var (
	//go:embed assets/index.html
	index []byte
	//go:embed assets/loader.mjs
	loader []byte
	//go:embed assets/style.css
	style []byte
	//go:embed assets/manifest.json
	manifest []byte
	//go:embed assets/archivo.woff2
	archivo []byte
	//go:embed assets/martian-mono.woff2
	martianMono []byte

	// GUI is the approved task interface. The owner CLI sends it over the
	// authenticated peer connection; A never serves it.
	//go:embed assets/gui.js
	GUI []byte
)

// CSP admits exactly the page's inline capture script and the loader module. The
// loader's verified GUI runs through 'strict-dynamic', and Trusted Types limits
// script URLs to the loader's policy. Fonts come only from A's own font routes, and
// images only from object URLs the GUI made for result previews.
var CSP = "default-src 'none'; " +
	"script-src " + scriptHash(inlineScript(index)) + " " + scriptHash(loader) + " 'strict-dynamic'; " +
	"style-src 'self'; font-src 'self'; img-src blob:; connect-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; worker-src 'none'; " +
	"require-trusted-types-for 'script'; trusted-types runlink-gui"

// TaskPath matches a task URL path, a slash and a lowercase UUID.
var TaskPath = regexp.MustCompile(`^/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Asset returns the file behind one of A's fixed public routes. Every other path
// stays closed; the GUI bundle is never served here.
func Asset(path string) (data []byte, contentType string, ok bool) {
	switch {
	case path == "/" || TaskPath.MatchString(path):
		return index, "text/html; charset=utf-8", true
	case path == "/assets/loader.mjs":
		return loader, "text/javascript; charset=utf-8", true
	case path == "/assets/style.css":
		return style, "text/css; charset=utf-8", true
	case path == "/assets/manifest.json":
		return manifest, "application/json", true
	case path == "/assets/archivo.woff2":
		return archivo, "font/woff2", true
	case path == "/assets/martian-mono.woff2":
		return martianMono, "font/woff2", true
	}
	return nil, "", false
}

func scriptHash(script []byte) string {
	sum := sha256.Sum256(script)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// inlineScript returns the body of the page's first plain <script> element, the
// capture script, so the CSP hashes exactly the bytes the page contains.
func inlineScript(page []byte) []byte {
	_, rest, opened := bytes.Cut(page, []byte("<script>"))
	script, _, closed := bytes.Cut(rest, []byte("</script>"))
	if !opened || !closed {
		panic("frontend: index.html lacks its inline capture script")
	}
	return script
}
