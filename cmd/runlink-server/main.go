// Command runlink-server runs the private app (B) or the trusted frontend (A).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"runlink/internal/buildinfo"
	"runlink/internal/server"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	c := server.DefaultConfig()
	opts, err := parseFlags(args, &c)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		// Parse errors quote argument values, which may be secrets.
		fmt.Fprintln(os.Stderr, "invalid options; use runlink-server --help")
		return 2
	case opts.version:
		fmt.Println("runlink-server", buildinfo.Version)
		return 0
	}
	if opts.configPath != "" {
		if c, err = server.LoadConfig(opts.configPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		// Parse again over the file's values so explicit flags override them.
		// The arguments already parsed once, so this cannot fail.
		_, _ = parseFlags(args, &c)
	}
	if err := c.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		logger.Error("listen_failed", "err", err)
		return 1
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, c, listener, logger); err != nil {
		logger.Error("server_failed", "err", err)
		return 1
	}
	return 0
}

type options struct {
	configPath string
	version    bool
}

// parseFlags binds the configuration flags onto c, so only flags given
// explicitly change it. It prints help itself and then returns flag.ErrHelp.
func parseFlags(args []string, c *server.Config) (options, error) {
	var opts options
	flags := flag.NewFlagSet("runlink-server", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.configPath, "config", "", "JSON configuration `file`; explicit flags override its values")
	flags.BoolVar(&opts.version, "version", false, "print version")
	flags.StringVar(&c.Role, "role", c.Role, "app or frontend")
	flags.StringVar(&c.Listen, "listen", c.Listen, "explicit `IP:port`; public addresses need the TLS frontend")
	flags.StringVar(&c.StateDir, "state-dir", c.StateDir, "private application state `directory` (app only)")
	flags.StringVar(&c.Upstream, "upstream", c.Upstream, "private application `origin` http://IP:port (frontend only)")
	flags.StringVar(&c.TLSCert, "tls-cert", c.TLSCert, "absolute TLS certificate `file` (frontend only)")
	flags.StringVar(&c.TLSKey, "tls-key", c.TLSKey, "absolute TLS private key `file` (frontend only)")
	err := flags.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println("Usage: runlink-server [options]")
		flags.SetOutput(os.Stdout)
		flags.PrintDefaults()
	case err == nil && flags.NArg() > 0:
		err = errors.New("unexpected arguments")
	}
	return opts, err
}
