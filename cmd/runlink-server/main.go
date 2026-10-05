package main

import (
	"context"
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

func main() { os.Exit(run()) }
func run() int {
	flags := flag.NewFlagSet("runlink-server", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "configuration JSON file")
	version := flags.Bool("version", false, "print version")
	c := server.DefaultConfig()
	role := flags.String("role", "", "app or frontend")
	listen := flags.String("listen", "", "loopback/private IP:port")
	state := flags.String("state-dir", "", "private application state directory")
	tlsCert := flags.String("tls-cert", "", "absolute TLS certificate file path (frontend only)")
	tlsKey := flags.String("tls-key", "", "absolute TLS private key file path (frontend only)")
	upstream := flags.String("upstream", "", "frontend's private application HTTP origin")
	flags.Usage = func() {}
	if err := flags.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			flags.SetOutput(os.Stdout)
			fmt.Fprintln(os.Stdout, "Usage: runlink-server [options]")
			flags.PrintDefaults()
			return 0
		}
		fmt.Fprintln(os.Stderr, "invalid options; use runlink-server --help")
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		return 2
	}
	if *version {
		fmt.Println("runlink-server", buildinfo.Version)
		return 0
	}
	var err error
	if *path != "" {
		c, err = server.LoadConfig(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "role":
			c.Role = *role
		case "listen":
			c.Listen = *listen
		case "state-dir":
			c.StateDir = *state
		case "tls-cert":
			c.TLSCert = *tlsCert
		case "tls-key":
			c.TLSKey = *tlsKey
		case "upstream":
			c.Upstream = *upstream
		}
	})
	if err := c.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		logger.Error("listen_failed")
		return 1
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, c, listener, logger); err != nil {
		logger.Error("server_failed")
		return 1
	}
	return 0
}
