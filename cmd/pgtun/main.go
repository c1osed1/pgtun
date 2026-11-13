// Command pgtun tunnels SOCKS5/Shadowsocks traffic through a real PostgreSQL
// database used as a rendezvous mailbox.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/c1osed1/pgtun/internal/client"
	"github.com/c1osed1/pgtun/internal/server"
	"github.com/c1osed1/pgtun/internal/sub"
)

// version is the release string printed by `pgtun --version` and
// `pgtun version`. It is injected at build time via
// -ldflags "-X main.version=..." and defaults to "dev".
var version = "dev"

const usageText = `pgtun - tunnel SOCKS5/Shadowsocks through a real PostgreSQL database

Usage:
  pgtun server [flags]    run the egress side (claims streams, dials targets)
  pgtun client [flags]    run the local side (SOCKS5 / Shadowsocks listeners)
  pgtun version           print version and exit

Common flags:
  --dsn string        PostgreSQL DSN (env PGTUN_DSN)

Server flags:
  --max-streams int   maximum concurrent streams (default 512)

Client flags:
  --config string         JSON config file (flags below override it)
  --dsn string            PostgreSQL DSN (env PGTUN_DSN)
  --socks string          SOCKS5 listen address (default "127.0.0.1:1080", "" disables)
  --ss string             Shadowsocks listen address (default "", disabled)
  --ss-method string      Shadowsocks AEAD method (default "chacha20-ietf-poly1305")
  --ss-password string    Shadowsocks password (env PGTUN_SS_PASSWORD)
  --max-streams int       maximum concurrent streams (default 256)
  --public-host string    override the detected LAN/public IP in the subscription
  --tag string            subscription name (default "pgtun")
  --print-sub             print ss:// URI + base64 subscription + QR and exit
                          (does not connect to the database)

Examples:
  pgtun client --config /etc/pgtun/client.json
  pgtun client --dsn "postgres://pgtun:secret@db:5432/postgres?sslmode=require" \
      --socks 127.0.0.1:1080 --ss 0.0.0.0:8388 --ss-password secret
  pgtun client --print-sub --ss 0.0.0.0:8388 --ss-password secret

Environment:
  PGTUN_DSN          default --dsn
  PGTUN_SS_PASSWORD  default --ss-password
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "--version", "-version", "version":
		fmt.Printf("pgtun %s\n", version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "server":
		err = runServer(ctx, os.Args[2:])
	case "client":
		err = runClient(ctx, os.Args[2:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usageText)
		return
	default:
		fmt.Fprintf(os.Stderr, "pgtun: unknown command %q\n\n%s", os.Args[1], usageText)
		os.Exit(2)
	}

	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "pgtun: %v\n", err)
		os.Exit(1)
	}
}

func runServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }

	dsn := fs.String("dsn", os.Getenv("PGTUN_DSN"), "PostgreSQL DSN")
	maxStreams := fs.Int("max-streams", 512, "maximum concurrent streams")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("server: --dsn or PGTUN_DSN is required")
	}

	return server.Run(ctx, server.Config{
		DSN:        *dsn,
		MaxStreams: *maxStreams,
	})
}

func runClient(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }

	configPath := fs.String("config", "", "path to JSON config file")
	dsn := fs.String("dsn", "", "PostgreSQL DSN (default PGTUN_DSN)")
	socksAddr := fs.String("socks", "", "SOCKS5 listen address (empty to disable)")
	ssAddr := fs.String("ss", "", "Shadowsocks listen address (empty to disable)")
	ssMethod := fs.String("ss-method", "", "Shadowsocks AEAD method")
	ssPassword := fs.String("ss-password", "", "Shadowsocks password")
	maxStreams := fs.Int("max-streams", 0, "maximum concurrent streams")
	publicHost := fs.String("public-host", "", "public/LAN host advertised in the subscription")
	printSub := fs.Bool("print-sub", false, "print subscription + QR and exit")
	tag := fs.String("tag", "pgtun", "subscription tag")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Only flags the user actually passed override the config file.
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var fc client.FileConfig
	if *configPath != "" {
		var err error
		fc, err = client.LoadConfigFile(*configPath)
		if err != nil {
			return err
		}
	}

	pickStr := func(name, flagVal string, fileVal *string, fallback string) string {
		if set[name] {
			return flagVal
		}
		if fileVal != nil {
			return *fileVal
		}
		return fallback
	}
	pickInt := func(name string, flagVal int, fileVal *int, fallback int) int {
		if set[name] {
			return flagVal
		}
		if fileVal != nil {
			return *fileVal
		}
		return fallback
	}

	cfg := client.Config{
		DSN:        pickStr("dsn", *dsn, fc.DSN, os.Getenv("PGTUN_DSN")),
		SocksAddr:  pickStr("socks", *socksAddr, fc.Socks, "127.0.0.1:1080"),
		SSAddr:     pickStr("ss", *ssAddr, fc.SS, ""),
		SSMethod:   pickStr("ss-method", *ssMethod, fc.SSMethod, "chacha20-ietf-poly1305"),
		SSPassword: pickStr("ss-password", *ssPassword, fc.SSPassword, os.Getenv("PGTUN_SS_PASSWORD")),
		MaxStreams: pickInt("max-streams", *maxStreams, fc.MaxStreams, 256),
		PublicHost: pickStr("public-host", *publicHost, fc.PublicHost, ""),
		Tag:        pickStr("tag", *tag, fc.Tag, "pgtun"),
	}

	if *printSub {
		if cfg.SSAddr == "" {
			return errors.New("client: --print-sub requires --ss (or \"ss\" in --config)")
		}
		if cfg.SSPassword == "" {
			return errors.New("client: --print-sub requires --ss-password (or \"ss_password\" in --config)")
		}
		host, port, err := sub.ParseListenAddr(cfg.SSAddr)
		if err != nil {
			return fmt.Errorf("client: --ss: %w", err)
		}
		return sub.Print(os.Stdout, sub.ResolveHost(host, cfg.PublicHost), port,
			cfg.SSMethod, cfg.SSPassword, cfg.Tag)
	}

	if cfg.DSN == "" {
		return errors.New("client: --dsn or PGTUN_DSN is required")
	}
	if cfg.SSAddr != "" && cfg.SSPassword == "" {
		return errors.New("client: --ss requires --ss-password or PGTUN_SS_PASSWORD")
	}

	return client.Run(ctx, cfg)
}
