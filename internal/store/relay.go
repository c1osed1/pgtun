package store

import (
	"context"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	dirClientToServer int16 = 0
	dirServerToClient int16 = 1

	relayBufSize  = 32 * 1024
	fetchLimit    = 512
	fetchInterval = 40 * time.Millisecond
)

// RelayClient pumps traffic between the client-side local conn and the mailbox
// chunks. local is a SOCKS/Shadowsocks client connection.
func RelayClient(ctx context.Context, st *Store, hub *Hub, id uuid.UUID, local net.Conn) {
	relay(ctx, st, hub, id, local, dirClientToServer, dirServerToClient)
}

// RelayServer pumps traffic between the egress target conn and the mailbox
// chunks.
func RelayServer(ctx context.Context, st *Store, hub *Hub, id uuid.UUID, local net.Conn) {
	relay(ctx, st, hub, id, local, dirServerToClient, dirClientToServer)
}

func relay(ctx context.Context, st *Store, hub *Hub, id uuid.UUID, local net.Conn, upDir, downDir int16) {
	var once sync.Once
	shutdown := func() { once.Do(func() { _ = local.Close() }) }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		upload(ctx, st, id, local, upDir, shutdown)
	}()
	go func() {
		defer wg.Done()
		download(ctx, st, hub, id, local, downDir, shutdown)
	}()
	wg.Wait()
	shutdown()

	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.MarkClosed(cctx, id); err != nil {
		log.Printf("pgtun: mark closed stream=%s: %v", id, err)
	}
}

func upload(ctx context.Context, st *Store, id uuid.UUID, local net.Conn, dir int16, shutdown func()) {
	buf := make([]byte, relayBufSize)
	for {
		n, err := local.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			if e := st.InsertChunk(ctx, id, dir, data); e != nil {
				if ctx.Err() == nil {
					log.Printf("pgtun: insert chunk stream=%s dir=%d: %v", id, dir, e)
				}
				shutdown()
				return
			}
		}
		if err != nil {
			// Read may return n>0 together with io.EOF: data was already sent.
			if e := st.InsertChunk(ctx, id, dir, nil); e != nil && ctx.Err() == nil {
				log.Printf("pgtun: insert eof stream=%s dir=%d: %v", id, dir, e)
			}
			if err != io.EOF {
				shutdown()
			}
			return
		}
	}
}

func download(ctx context.Context, st *Store, hub *Hub, id uuid.UUID, local net.Conn, dir int16, shutdown func()) {
	ch, unreg := hub.Register(id)
	defer unreg()

	var lastID int64
	ticker := time.NewTicker(fetchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			shutdown()
			return
		case _, ok := <-ch:
			if !ok {
				ch = nil
			}
		case <-ticker.C:
		}

		chunks, err := st.FetchChunks(ctx, id, dir, lastID, fetchLimit)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("pgtun: fetch stream=%s dir=%d: %v", id, dir, err)
			}
			shutdown()
			return
		}
		for _, c := range chunks {
			lastID = c.ID
			if c.Data == nil {
				shutdown()
				return
			}
			if err := writeFull(local, c.Data); err != nil {
				shutdown()
				return
			}
		}
	}
}

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}
