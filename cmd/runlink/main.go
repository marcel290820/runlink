// Command runlink is the owner CLI. Publishing is not implemented yet.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"runlink/internal/buildinfo"
)

const usage = `runlink turns a script on this machine into a link that runs it from a browser.
Publishing is not available yet.

Usage:
  runlink <command>

Commands:
  status    Check that the Runlink service is reachable
  version   Print the version
  help      Show this help

Environment:
  RUNLINK_SERVICE   Service origin; this build defaults to %s
`

// A status response is one small JSON object; statusText bounds what a server
// can make the CLI print.
const maxStatusBody = 4096

var statusText = regexp.MustCompile(`^[a-z_]{1,64}$`)

// Service errors are fixed text, so they never echo the configured value.
var (
	errOrigin = errors.New("runlink: RUNLINK_SERVICE must be an origin such as https://runlink.dev")
	errScheme = errors.New("runlink: RUNLINK_SERVICE must use https, or http with a loopback IP")
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"help"}
	}
	if len(args) == 1 {
		switch args[0] {
		case "help", "--help", "-h":
			fmt.Fprintf(stdout, usage, buildinfo.Service)
			return 0
		case "version", "--version":
			fmt.Fprintln(stdout, "runlink", buildinfo.Version)
			return 0
		case "status":
			origin, err := service(getenv)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			return status(origin, stdout, stderr)
		}
	}
	fmt.Fprintln(stderr, "runlink: unknown command; run runlink help")
	return 2
}

// service returns the origin of the Runlink frontend: RUNLINK_SERVICE, or the
// build's default. Plain HTTP is accepted only for a loopback IP, the local
// stack.
func service(getenv func(string) string) (string, error) {
	origin := getenv("RUNLINK_SERVICE")
	if origin == "" {
		origin = buildinfo.Service
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (&url.URL{Scheme: u.Scheme, Host: u.Host}).String() != origin {
		return "", errOrigin
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || err != nil || !ip.IsLoopback()) {
		return "", errScheme
	}
	return origin, nil
}

// status reports the service's status route through the frontend, the path
// the owner connection will take.
func status(origin string, stdout, stderr io.Writer) int {
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Get(origin + "/api/v1/status")
	if err != nil {
		hint := ""
		if strings.HasPrefix(origin, "http:") {
			hint = "; start the local stack with scripts/dev.sh"
		}
		fmt.Fprintf(stderr, "runlink: %s is unreachable%s\n", origin, hint)
		return 1
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxStatusBody+1))
	var body struct {
		Status string `json:"status"`
	}
	if err != nil || res.StatusCode != http.StatusOK || len(data) > maxStatusBody ||
		json.Unmarshal(data, &body) != nil || !statusText.MatchString(body.Status) {
		fmt.Fprintf(stderr, "runlink: %s returned an unexpected response\n", origin)
		return 1
	}
	fmt.Fprintf(stdout, "%s: %s\n", origin, body.Status)
	return 0
}
