// Package server implements the egress side of pgtun: it claims streams from
// the PostgreSQL mailbox, dials the requested targets and relays traffic.
package server

import (
	"context"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/c1osed1/pgtun/internal/store"
)

// Config configures the egress server.
type Config struct {
	DSN        string
	MaxStreams int
}

const (
	claimInterval = 200 * time.Millisecond
	claimBatch    = 64
	dialTimeout   = 10 * time.Second
)

// Run claims and serves streams until ctx is done.
func Run(ctx context.Context, cfg Config) error {
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = 512
	}

	st, err := store.New(ctx, cfg.DSN, 32)
	if err != nil {
		return err
	}
	defer st.Close()

	hub := st.Listen(ctx)
	defer hub.Close()

	go st.Janitor(ctx, time.Minute)

	var active atomic.Int64
	ticker := time.NewTicker(claimInterval)
	defer ticker.Stop()

	log.Printf("pgtun: server: egress ready, max streams %d", cfg.MaxStreams)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		room := cfg.MaxStreams - int(active.Load())
		if room <= 0 {
			continue
		}
		limit := claimBatch
		if limit > room {
			limit = room
		}

		streams, err := st.ClaimStreams(ctx, limit)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("pgtun: server: claim: %v", err)
			}
			continue
		}

		for _, s := range streams {
			active.Add(1)
			go func(s store.Stream) {
				defer active.Add(-1)
				conn, err := net.DialTimeout("tcp", s.Target, dialTimeout)
				if err != nil {
					log.Printf("pgtun: server: dial %s (stream=%s): %v", s.Target, s.ID, err)
					cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_ = st.MarkClosed(cctx, s.ID)
					cancel()
					return
				}
				log.Printf("pgtun: server: stream=%s target=%s", s.ID, s.Target)
				store.RelayServer(ctx, st, hub, s.ID, conn)
				_ = conn.Close()
			}(s)
		}
	}
}
