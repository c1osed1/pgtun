// Package client implements the local side of pgtun: SOCKS5 and Shadowsocks
// listeners that multiplex streams through PostgreSQL.
package client

import (
	"context"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/c1osed1/pgtun/internal/socks"
	"github.com/c1osed1/pgtun/internal/ss"
	"github.com/c1osed1/pgtun/internal/store"
	"github.com/c1osed1/pgtun/internal/sub"
)

// Config configures the client.
type Config struct {
	DSN        string
	SocksAddr  string
	SSAddr     string
	SSMethod   string
	SSPassword string
	MaxStreams int

	// PublicHost overrides the LAN/public IP advertised in the subscription.
	PublicHost string
	// Tag is the SIP002 fragment (subscription name).
	Tag string
}

// Run starts the configured listeners and blocks until ctx is done.
func Run(ctx context.Context, cfg Config) error {
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = 256
	}
	if cfg.SSMethod == "" {
		cfg.SSMethod = "chacha20-ietf-poly1305"
	}
	if cfg.Tag == "" {
		cfg.Tag = "pgtun"
	}

	st, err := store.New(ctx, cfg.DSN, 32)
	if err != nil {
		return err
	}
	defer st.Close()

	hub := st.Listen(ctx)
	defer hub.Close()

	var active atomic.Int64
	handle := func(target string, conn net.Conn) {
		if active.Load() >= int64(cfg.MaxStreams) {
			log.Printf("pgtun: client: max streams (%d) reached, dropping %s", cfg.MaxStreams, target)
			return
		}
		active.Add(1)
		defer active.Add(-1)

		id := uuid.New()
		if err := st.CreateStream(ctx, id, target); err != nil {
			log.Printf("pgtun: client: create stream -> %s: %v", target, err)
			return
		}
		log.Printf("pgtun: client: stream=%s target=%s", id, target)
		store.RelayClient(ctx, st, hub, id, conn)
	}

	var wg sync.WaitGroup
	var listeners []net.Listener

	if cfg.SocksAddr != "" {
		ln, err := net.Listen("tcp", cfg.SocksAddr)
		if err != nil {
			return err
		}
		listeners = append(listeners, ln)
		log.Printf("pgtun: client: SOCKS5 listening on %s", ln.Addr())
		wg.Add(1)
		go func() {
			defer wg.Done()
			socks.Serve(ctx, ln, handle)
		}()
	}

	if cfg.SSAddr != "" {
		ln, err := net.Listen("tcp", cfg.SSAddr)
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return err
		}
		listeners = append(listeners, ln)
		log.Printf("pgtun: client: Shadowsocks (%s) listening on %s", cfg.SSMethod, ln.Addr())
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ss.Serve(ctx, ln, cfg.SSMethod, cfg.SSPassword, handle); err != nil {
				log.Printf("pgtun: client: ss: %v", err)
			}
		}()
	}

	if cfg.SocksAddr == "" && cfg.SSAddr == "" {
		return errNoListeners
	}

	if cfg.SSAddr != "" {
		if host, port, err := sub.ParseListenAddr(cfg.SSAddr); err != nil {
			log.Printf("pgtun: client: cannot build subscription for %q: %v", cfg.SSAddr, err)
		} else if err := sub.Print(os.Stdout, sub.ResolveHost(host, cfg.PublicHost), port,
			cfg.SSMethod, cfg.SSPassword, cfg.Tag); err != nil {
			log.Printf("pgtun: client: print subscription: %v", err)
		}
	}

	<-ctx.Done()
	for _, l := range listeners {
		_ = l.Close()
	}
	wg.Wait()
	return nil
}

type configError string

func (e configError) Error() string { return string(e) }

const errNoListeners = configError("client: no listeners configured (set --socks and/or --ss)")
