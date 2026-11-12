package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

const (
	rollupStep  = int64(10_000)    // ms
	hourMs      = int64(3_600_000) // ms
	slotsPerRow = int(hourMs / rollupStep)
)

// InsertSamples stores raw values of an agent's samples in one transaction.
func (s *Store) InsertSamples(ctx context.Context, agentID int64, samples []model.Sample) error {
	if len(samples) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ins, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO raw(series_id,ts,v) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()

	cache := s.series[agentID]
	if cache == nil {
		if cache, err = s.loadSeries(ctx, tx, agentID); err != nil {
			return err
		}
	}
	created := map[string]int64{}
	minTS, maxTS := int64(math.MaxInt64), int64(math.MinInt64)
	for i := range samples {
		ts := samples[i].TS
		for key, v := range samples[i].Flatten() {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			id, ok := cache[key]
			if !ok {
				if id, ok = created[key]; !ok {
					if id, err = createSeries(ctx, tx, agentID, key); err != nil {
						return err
					}
					created[key] = id
				}
			}
			if _, err := ins.ExecContext(ctx, id, ts, v); err != nil {
				return err
			}
		}
		minTS, maxTS = min(minTS, ts), max(maxTS, ts)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for k, id := range created { // publish new ids only after commit
		cache[k] = id
	}
	s.series[agentID] = cache
	if r, ok := s.dirty[agentID]; ok {
		minTS, maxTS = min(minTS, r[0]), max(maxTS, r[1])
	}
	s.dirty[agentID] = [2]int64{minTS, maxTS}
	return nil
}

func (s *Store) loadSeries(ctx context.Context, tx *sql.Tx, agentID int64) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,key FROM series WHERE agent_id=?`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int64{}
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			return nil, err
		}
		m[k] = id
	}
	return m, rows.Err()
}

func createSeries(ctx context.Context, tx *sql.Tx, agentID int64, key string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO series(agent_id,key) VALUES(?,?)`, agentID, key); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM series WHERE agent_id=? AND key=?`, agentID, key).Scan(&id)
	return id, err
}

// Maintain rolls up newly ingested raw data into 10 s averages and then
// deletes data older than the retention periods. Run it every minute.
func (s *Store) Maintain(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	dirty := s.dirty
	s.dirty = map[int64][2]int64{}
	s.mu.Unlock()

	var firstErr error
	for agent, r := range dirty {
		if err := s.rollupRange(ctx, agent, r[0], r[1]); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.mu.Lock() // retry next time
			if d, ok := s.dirty[agent]; ok {
				r = [2]int64{min(r[0], d[0]), max(r[1], d[1])}
			}
			s.dirty[agent] = r
			s.mu.Unlock()
		}
	}

	// Never purge raw rows that are still waiting to be rolled up.
	cutoff := now.Add(-s.RawRetention).UnixMilli()
	s.mu.Lock()
	for _, r := range s.dirty {
		cutoff = min(cutoff, r[0]/rollupStep*rollupStep)
	}
	s.mu.Unlock()
	if err := s.purgeRaw(ctx, cutoff); err != nil && firstErr == nil {
		firstErr = err
	}
	histCut := now.Add(-s.HistoryRetention).UnixMilli() / hourMs * hourMs
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rollup WHERE hour < ?`, histCut); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *Store) rollupRange(ctx context.Context, agentID, fromTS, toTS int64) error {
	from := fromTS / rollupStep * rollupStep
	to := toTS/rollupStep*rollupStep + rollupStep
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.series_id, (r.ts / ?) * ? AS b, avg(r.v)
		FROM series s JOIN raw r ON r.series_id = s.id
		WHERE s.agent_id = ? AND substr(s.key, 1, ?) != ? AND r.ts >= ? AND r.ts < ?
		GROUP BY r.series_id, b`,
		rollupStep, rollupStep, agentID, len(model.NoRollupPrefix), model.NoRollupPrefix, from, to)
	if err != nil {
		return err
	}
	type rowKey struct{ series, hour int64 }
	upd := map[rowKey]map[int]float32{}
	for rows.Next() {
		var sid, b int64
		var v float64
		if err := rows.Scan(&sid, &b, &v); err != nil {
			rows.Close()
			return err
		}
		k := rowKey{sid, b / hourMs * hourMs}
		if upd[k] == nil {
			upd[k] = map[int]float32{}
		}
		upd[k][int((b-k.hour)/rollupStep)] = float32(v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, slots := range upd {
		var blob []byte
		err := tx.QueryRowContext(ctx, `SELECT data FROM rollup WHERE series_id=? AND hour=?`, k.series, k.hour).Scan(&blob)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		vals := decodeSlots(blob)
		for i, v := range slots {
			vals[i] = v
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO rollup(series_id,hour,data) VALUES(?,?,?)
			ON CONFLICT(series_id,hour) DO UPDATE SET data=excluded.data`, k.series, k.hour, encodeSlots(vals)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) purgeRaw(ctx context.Context, cutoff int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM series`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	// Per-series deletes use the (series_id, ts) primary key instead of
	// scanning the whole table.
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM raw WHERE series_id=? AND ts<?`, id, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// decodeSlots returns 360 values; missing data is NaN.
func decodeSlots(b []byte) []float32 {
	v := make([]float32, slotsPerRow)
	for i := range v {
		if len(b) >= (i+1)*4 {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		} else {
			v[i] = float32(math.NaN())
		}
	}
	return v
}

func encodeSlots(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

// Point is [unix ms, value].
type Point [2]float64

// History returns up to ~points averaged points per key in [from, to] (ms).
// Ranges that start within the last hour are served from raw data, longer
// ones from the 10 s rollups.
func (s *Store) History(ctx context.Context, agentID int64, keys []string, from, to int64, points int, now time.Time) (map[string][]Point, error) {
	if points <= 0 || points > 5000 {
		points = 600
	}
	useRaw := from >= now.Add(-time.Hour).UnixMilli()
	native := int64(1000)
	if !useRaw {
		native = rollupStep
	}
	bucket := max(native, (to-from)/int64(points))
	bucket = (bucket + native - 1) / native * native

	out := make(map[string][]Point, len(keys))
	for _, key := range keys {
		var sid int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM series WHERE agent_id=? AND key=?`, agentID, key).Scan(&sid)
		if errors.Is(err, sql.ErrNoRows) {
			out[key] = []Point{}
			continue
		}
		if err != nil {
			return nil, err
		}
		agg := newBucketer(bucket)
		if useRaw {
			rows, err := s.db.QueryContext(ctx, `SELECT ts, v FROM raw WHERE series_id=? AND ts>=? AND ts<=? ORDER BY ts`, sid, from, to)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var ts int64
				var v float64
				if err := rows.Scan(&ts, &v); err != nil {
					rows.Close()
					return nil, err
				}
				agg.add(ts, v)
			}
			rows.Close()
		} else {
			rows, err := s.db.QueryContext(ctx, `SELECT hour, data FROM rollup WHERE series_id=? AND hour>=? AND hour<=? ORDER BY hour`,
				sid, from/hourMs*hourMs, to)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var hour int64
				var blob []byte
				if err := rows.Scan(&hour, &blob); err != nil {
					rows.Close()
					return nil, err
				}
				for i, v := range decodeSlots(blob) {
					ts := hour + int64(i)*rollupStep
					if ts < from || ts > to || math.IsNaN(float64(v)) {
						continue
					}
					agg.add(ts, float64(v))
				}
			}
			rows.Close()
		}
		out[key] = agg.points()
	}
	return out, nil
}

// SeriesKeys lists the stored series names of an agent matching prefix.
func (s *Store) SeriesKeys(ctx context.Context, agentID int64, prefix string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM series WHERE agent_id=? ORDER BY key`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, rows.Err()
}

// bucketer averages ascending (ts, v) pairs into fixed-width buckets.
type bucketer struct {
	width   int64
	cur     int64
	sum     float64
	n       int
	started bool
	out     []Point
}

func newBucketer(width int64) *bucketer { return &bucketer{width: width} }

func (b *bucketer) add(ts int64, v float64) {
	k := ts / b.width * b.width
	if b.started && k != b.cur {
		b.flush()
	}
	b.cur, b.started = k, true
	b.sum += v
	b.n++
}

func (b *bucketer) flush() {
	if b.n > 0 {
		b.out = append(b.out, Point{float64(b.cur), math.Round(b.sum/float64(b.n)*100) / 100})
	}
	b.sum, b.n = 0, 0
}

func (b *bucketer) points() []Point {
	b.flush()
	if b.out == nil {
		return []Point{}
	}
	return b.out
}
