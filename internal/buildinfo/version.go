// Package buildinfo carries metadata that scripts/release.py stamps into release builds.
package buildinfo

// Version is set by release builds; development builds report "dev".
var Version = "dev"

// Service is the origin the owner CLI uses unless RUNLINK_SERVICE overrides it.
// Development builds target the local stack that scripts/dev.sh runs; release
// builds stamp https://runlink.dev.
var Service = "http://127.0.0.1:8080"
