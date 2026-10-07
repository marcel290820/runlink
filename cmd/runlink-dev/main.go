// Command runlink-dev runs the whole product on loopback for local development:
// the trusted frontend (A), the app (B), and a TURN stand-in for coturn (C).
// It is a development tool and never ships in releases (ADR 0008).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"runlink/internal/buildinfo"
	"runlink/internal/devstack"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	c := devstack.DefaultConfig()
	var version bool
	flags := flag.NewFlagSet("runlink-dev", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&version, "version", false, "print version")
	flags.StringVar(&c.Frontend, "frontend", c.Frontend, "loopback `IP:port` of the frontend (A)")
	flags.StringVar(&c.App, "app", c.App, "loopback `IP:port` of the app (B)")
	flags.StringVar(&c.TURN, "turn", c.TURN, "loopback `IP:port` of STUN/TURN over UDP and TCP (C)")
	flags.StringVar(&c.StateDir, "state-dir", c.StateDir, "private development state `directory`")
	err := flags.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println("Usage: runlink-dev [options]\n\nRun the frontend, app, and a TURN stand-in on loopback until interrupted.")
		flags.SetOutput(os.Stdout)
		flags.PrintDefaults()
		return 0
	case err != nil || flags.NArg() > 0:
		// Parse errors can quote argument values.
		fmt.Fprintln(os.Stderr, "invalid options; use runlink-dev --help")
		return 2
	case version:
		fmt.Println("runlink-dev", buildinfo.Version)
		return 0
	}
	if err := c.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "runlink-dev: %v; use runlink-dev --help\n", err)
		return 2
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stack, err := devstack.Start(ctx, c, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runlink-dev: %v\n", err)
		return 1
	}
	logger.Info("stack_ready", "frontend", stack.Frontend, "app", stack.App, "turn", stack.TURN)
	printSummary(stack, c.StateDir)
	if err := stack.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "runlink-dev: %v\n", err)
		return 1
	}
	return 0
}

func printSummary(stack *devstack.Stack, stateDir string) {
	cli := "build/runlink status"
	if stack.Frontend != buildinfo.Service {
		cli = "RUNLINK_SERVICE=" + stack.Frontend + " " + cli
	}
	fmt.Printf(`Runlink local stack: production services, infrastructure stand-ins (ADR 0008)

  A  frontend  %-24s loader and ingress, in place of https://runlink.dev
  B  app       %-24s registration and signaling, private behind A
  C  turn      %-24s STUN/TURN stand-in for coturn, UDP and TCP
               %s
  state        %s, TURN shared secret in %s

Owner CLI:  %s
Ctrl-C stops all three.
`, stack.Frontend, stack.App, stack.TURN, strings.Join(stack.TURNURIs, " "), stateDir, filepath.Join(stateDir, "turn-secret"), cli)
}
