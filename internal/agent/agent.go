package agent

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"monitor.uz/internal/collector"
	"monitor.uz/internal/model"
)

// Config controls the agent loop.
type Config struct {
	StateDir   string
	Interval   time.Duration // collection interval (1 s for host)
	BufferSize int           // max samples kept while the hub is unreachable
	BatchSize  int           // max samples per request
	Version    string
	Mode       string
}

// Run collects samples every Interval and pushes them to the hub until ctx
// is cancelled. Undelivered samples are saved to disk on exit.
func Run(ctx context.Context, cfg Config, st *State, log *slog.Logger) error {
	client, err := NewClient(st.Hub, st.Pin, st.Token)
	if err != nil {
		return err
	}
	buf := NewBuffer(cfg.BufferSize)
	bufFile := filepath.Join(cfg.StateDir, "buffer.json.gz")
	if n, err := buf.Load(bufFile); err != nil {
		log.Warn("could not restore buffer", "err", err)
	} else if n > 0 {
		log.Info("restored buffered samples", "count", n)
	}

	col := collector.NewHost()
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sendLoop(ctx, cfg, client, buf, wake, log)
	}()

	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	col.Collect(time.Now()) // prime counters; first sample has no rates
	for {
		select {
		case <-ctx.Done():
			<-done
			if err := buf.Save(bufFile); err != nil {
				log.Error("saving buffer failed", "err", err)
			} else if n := buf.Len(); n > 0 {
				log.Info("saved undelivered samples", "count", n)
			}
			return nil
		case <-t.C:
			// time.Now() carries a monotonic reading; Collect divides
			// counter deltas by the real elapsed time, not by Interval.
			s := col.Collect(time.Now())
			buf.Push(s)
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
}

func sendLoop(ctx context.Context, cfg Config, c *Client, buf *Buffer, wake <-chan struct{}, log *slog.Logger) {
	info := collector.ReadInfo("/proc", cfg.Mode, cfg.Version)
	var infoSent time.Time
	backoff := time.Duration(0)
	failing := false
	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
		}
		for buf.Len() > 0 {
			batch := buf.Peek(cfg.BatchSize)
			req := &model.IngestRequest{Samples: batch}
			if time.Since(infoSent) > time.Minute {
				inf := info
				req.Info = &inf
			}
			rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.Ingest(rctx, req)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				backoff = nextBackoff(backoff, errors.Is(err, ErrUnauthorized))
				if !failing {
					log.Warn("hub unreachable, buffering", "err", err, "buffered", buf.Len())
					failing = true
				}
				break
			}
			if req.Info != nil {
				infoSent = time.Now()
			}
			buf.Ack(batch[len(batch)-1].TS)
			if failing {
				log.Info("hub reachable again", "pending", buf.Len(), "dropped_total", buf.Dropped())
				failing = false
			}
			backoff = 0
		}
	}
}

// nextBackoff grows 1s,2s,4s… up to 10s (5 min when the token is rejected,
// so a revoked agent does not hammer the hub).
func nextBackoff(cur time.Duration, unauthorized bool) time.Duration {
	if unauthorized {
		return 5 * time.Minute
	}
	if cur <= 0 {
		return time.Second
	}
	return min(cur*2, 10*time.Second)
}
