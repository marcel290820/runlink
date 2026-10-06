package frontend

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

// The page must pin the exact embedded stylesheet and loader, or browsers refuse
// them; scripts/assets.py --check also catches this before release.
func TestPageIntegrityMatchesEmbeddedAssets(t *testing.T) {
	for name, data := range map[string][]byte{"style.css": style, "loader.mjs": loader} {
		sum := sha256.Sum256(data)
		attribute := `integrity="sha256-` + base64.StdEncoding.EncodeToString(sum[:]) + `"`
		if !bytes.Contains(index, []byte(attribute)) {
			t.Errorf("index.html does not pin %s; run python3 scripts/assets.py", name)
		}
	}
}

func TestCSPAdmitsOnlyCaptureAndLoaderScripts(t *testing.T) {
	if strings.Count(CSP, "'sha256-") != 2 {
		t.Fatalf("CSP must hash exactly two scripts: %s", CSP)
	}
	for _, script := range [][]byte{inlineScript(index), loader} {
		if !strings.Contains(CSP, scriptHash(script)) {
			t.Fatalf("CSP lacks %s", scriptHash(script))
		}
	}
	if !bytes.Contains(inlineScript(index), []byte("window.runlinkSession")) {
		t.Fatal("inline script is not the capture script")
	}
	for _, unsafe := range []string{"'unsafe-inline'", "'unsafe-eval'", "*"} {
		if strings.Contains(CSP, unsafe) {
			t.Fatalf("CSP contains %s", unsafe)
		}
	}
}
