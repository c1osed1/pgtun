// Package store implements the PostgreSQL-backed rendezvous mailbox used by
// pgtun. All tunnel state (streams and chunks) lives in the pgtun schema of a
// real PostgreSQL database.
package store

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps a pgx pool.
type Store struct {
	Pool *pgxpool.Pool

	dsn string
}

// Stream is a tunnel stream row.
type Stream struct {
	ID     uuid.UUID
	Target string
}

// Chunk is one chunk of tunnel payload. Data == nil is the EOF sentinel.
type Chunk struct {
	ID   int64
	Data []byte
}

// New opens a pool and verifies connectivity.
func New(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool, dsn: dsn}, nil
}

// Close releases the pool.
func (s *Store) Close() {
	if s != nil && s.Pool != nil {
		s.Pool.Close()
	}
}

// CreateStream registers a new stream for target.
func (s *Store) CreateStream(ctx context.Context, id uuid.UUID, target string) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO pgtun.streams (id, target) VALUES ($1::uuid, $2)`,
		id.String(), target)
	return err
}

// ClaimStreams atomically claims up to limit unclaimed streams (server side).
func (s *Store) ClaimStreams(ctx context.Context, limit int) ([]Stream, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		WITH c AS (
			SELECT id, target FROM pgtun.streams
			WHERE claimed = false AND closed = false
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE pgtun.streams s
		SET claimed = true
		FROM c
		WHERE s.id = c.id
		RETURNING s.id::text, s.target`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Stream
	for rows.Next() {
		var idText, target string
		if err := rows.Scan(&idText, &target); err != nil {
			return nil, err
		}
		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, err
		}
		out = append(out, Stream{ID: id, Target: target})
	}
	return out, rows.Err()
}

// MarkClosed marks a stream closed.
func (s *Store) MarkClosed(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE pgtun.streams SET closed = true WHERE id = $1::uuid`,
		id.String())
	return err
}

// IsClosed reports whether the stream is closed.
func (s *Store) IsClosed(ctx context.Context, id uuid.UUID) (bool, error) {
	var closed bool
	err := s.Pool.QueryRow(ctx,
		`SELECT closed FROM pgtun.streams WHERE id = $1::uuid`,
		id.String()).Scan(&closed)
	return closed, err
}

// InsertChunk appends a chunk (nil data = EOF) and wakes listeners in the same
// transaction. The notification payload is the stream UUID.
func (s *Store) InsertChunk(ctx context.Context, id uuid.UUID, dir int16, data []byte) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`INSERT INTO pgtun.chunks (stream_id, dir, data) VALUES ($1::uuid, $2, $3)`,
		id.String(), dir, data); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('pgtun', $1)`, id.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FetchChunks returns chunks after afterID in order and deletes the returned
// rows, all in one transaction.
func (s *Store) FetchChunks(ctx context.Context, id uuid.UUID, dir int16, afterID int64, limit int) ([]Chunk, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	rows, err := tx.Query(ctx, `
		SELECT id, data FROM pgtun.chunks
		WHERE stream_id = $1::uuid AND dir = $2 AND id > $3
		ORDER BY id
		LIMIT $4`, id.String(), dir, afterID, limit)
	if err != nil {
		return nil, err
	}

	var out []Chunk
	for rows.Next() {
		var (
			cid  int64
			data []byte
		)
		if err := rows.Scan(&cid, &data); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, Chunk{ID: cid, Data: data})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(out) > 0 {
		last := out[len(out)-1].ID
		if _, err := tx.Exec(ctx, `
			DELETE FROM pgtun.chunks
			WHERE stream_id = $1::uuid AND dir = $2 AND id <= $3`,
			id.String(), dir, last); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// Janitor removes stale chunks and closed streams older than 5 minutes.
func (s *Store) Janitor(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.Pool.Exec(ctx,
				`DELETE FROM pgtun.chunks WHERE created_at < now() - interval '5 minutes'`); err != nil {
				if ctx.Err() == nil {
					log.Printf("pgtun: janitor chunks: %v", err)
				}
			}
			if _, err := s.Pool.Exec(ctx,
				`DELETE FROM pgtun.streams WHERE closed = true AND created_at < now() - interval '5 minutes'`); err != nil {
				if ctx.Err() == nil {
					log.Printf("pgtun: janitor streams: %v", err)
				}
			}
		}
	}
}

// Hub dispatches LISTEN/NOTIFY wakeups to per-stream channels.
type Hub struct {
	mu     sync.RWMutex
	chans  map[uuid.UUID]map[chan struct{}]struct{}
	closed bool

	cancel context.CancelFunc
	done   chan struct{}
	dsn    string
}

// Listen opens a dedicated pgx connection (not from the pool), runs
// LISTEN pgtun and dispatches notifications until ctx is cancelled or Close is
// called. The connection is re-established with a 1s backoff on errors.
func (s *Store) Listen(ctx context.Context) *Hub {
	h := &Hub{
		chans: make(map[uuid.UUID]map[chan struct{}]struct{}),
		done:  make(chan struct{}),
		dsn:   s.dsn,
	}
	lctx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	go h.loop(lctx)
	return h
}

func (h *Hub) loop(ctx context.Context) {
	defer close(h.done)
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := pgx.Connect(ctx, h.dsn)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("pgtun: listen connect: %v", err)
			if !sleepCtx(ctx, time.Second) {
				return
			}
			continue
		}

		if _, err := conn.Exec(ctx, "LISTEN pgtun"); err != nil {
			_ = conn.Close(context.Background())
			if ctx.Err() != nil {
				return
			}
			log.Printf("pgtun: LISTEN: %v", err)
			if !sleepCtx(ctx, time.Second) {
				return
			}
			continue
		}

		for {
			n, err := conn.WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("pgtun: listen wait: %v", err)
				}
				break
			}
			if n == nil {
				continue
			}
			id, err := uuid.Parse(n.Payload)
			if err != nil {
				continue
			}
			h.dispatch(id)
		}

		_ = conn.Close(context.Background())
		if ctx.Err() != nil {
			return
		}
		if !sleepCtx(ctx, time.Second) {
			return
		}
	}
}

// Register returns a wakeup channel for a stream and its unregister function.
func (h *Hub) Register(id uuid.UUID) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ch, func() {}
	}
	m := h.chans[id]
	if m == nil {
		m = make(map[chan struct{}]struct{})
		h.chans[id] = m
	}
	m[ch] = struct{}{}
	h.mu.Unlock()

	unreg := func() {
		h.mu.Lock()
		if m, ok := h.chans[id]; ok {
			delete(m, ch)
			if len(m) == 0 {
				delete(h.chans, id)
			}
		}
		h.mu.Unlock()
	}
	return ch, unreg
}

func (h *Hub) dispatch(id uuid.UUID) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.chans[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Close stops the listener and releases registered channels.
func (h *Hub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	for id, m := range h.chans {
		for ch := range m {
			close(ch)
		}
		delete(h.chans, id)
	}
	h.mu.Unlock()

	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
