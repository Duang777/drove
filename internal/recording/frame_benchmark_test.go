package recording

import (
	"bytes"
	"context"
	"math/rand"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

const (
	frameBenchmarkBytes   = 50 << 20
	frameBenchmarkTargets = 16
	frameBenchmarkWarmSet = 1
)

func TestNearestRankDuration(t *testing.T) {
	samples := []time.Duration{
		time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
		4 * time.Millisecond,
		5 * time.Millisecond,
	}
	if got := nearestRankDuration(samples, 50); got != 3*time.Millisecond {
		t.Fatalf("p50 = %s, want 3ms", got)
	}
	if got := nearestRankDuration(samples, 95); got != 5*time.Millisecond {
		t.Fatalf("p95 = %s, want 5ms", got)
	}
}

func BenchmarkFrame50MiBColdRandom(b *testing.B) {
	st, lastSequence := newFrameBenchmarkStore(b)
	archive := NewArchive(st, newTailClock(lastSequence))
	archive.frames = newFrameCache(0)
	targets := randomFrameBenchmarkOffsets(frameBenchmarkTargets)
	durations := make([]time.Duration, 0, b.N)
	var replayed uint64

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		offset := targets[index%len(targets)]
		selector, err := NewSelector(SelectorInput{Offset: &offset})
		if err != nil {
			b.Fatalf("create cold frame selector: %v", err)
		}
		startedAt := time.Now()
		frame, err := archive.Frame(context.Background(), "benchmark", &selector)
		durations = append(durations, time.Since(startedAt))
		if err != nil {
			b.Fatalf("render cold frame at offset %s: %v", offset, err)
		}
		if frame.Cursor.NextOffset != offset {
			b.Fatalf("cold frame offset = %s, want %s", frame.Cursor.NextOffset, offset)
		}
		replayed += uint64(offset)
	}
	b.StopTimer()
	reportFrameBenchmarkMetrics(b, durations, replayed)
}

func BenchmarkFrame50MiBWarmRandom(b *testing.B) {
	st, lastSequence := newFrameBenchmarkStore(b)
	archive := NewArchive(st, newTailClock(lastSequence))
	targets := randomFrameBenchmarkOffsets(frameBenchmarkWarmSet)
	selectors := make([]Selector, len(targets))
	for index, offset := range targets {
		selector, err := NewSelector(SelectorInput{Offset: &offset})
		if err != nil {
			b.Fatalf("create warm frame selector: %v", err)
		}
		selectors[index] = selector
		if _, err := archive.Frame(
			context.Background(),
			"benchmark",
			&selectors[index],
		); err != nil {
			b.Fatalf("prime warm frame at offset %s: %v", offset, err)
		}
	}

	durations := make([]time.Duration, 0, b.N)
	var replayed uint64
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		targetIndex := index % len(selectors)
		startedAt := time.Now()
		frame, err := archive.Frame(
			context.Background(),
			"benchmark",
			&selectors[targetIndex],
		)
		durations = append(durations, time.Since(startedAt))
		if err != nil {
			b.Fatalf(
				"render warm frame at offset %s: %v",
				targets[targetIndex],
				err,
			)
		}
		if frame.Cursor.NextOffset != targets[targetIndex] {
			b.Fatalf(
				"warm frame offset = %s, want %s",
				frame.Cursor.NextOffset,
				targets[targetIndex],
			)
		}
		replayed += uint64(targets[targetIndex])
	}
	b.StopTimer()
	reportFrameBenchmarkMetrics(b, durations, replayed)
}

func newFrameBenchmarkStore(b *testing.B) (*store.Store, uint64) {
	b.Helper()
	st, err := store.Open(filepath.Join(b.TempDir(), "frame-benchmark.db"))
	if err != nil {
		b.Fatalf("open frame benchmark store: %v", err)
	}
	b.Cleanup(func() {
		if err := st.Close(); err != nil {
			b.Errorf("close frame benchmark store: %v", err)
		}
	})

	chunk := frameBenchmarkChunk()
	chunkCount := frameBenchmarkBytes / event.MaxOutputChunkBytes
	rows := make([]store.EventRow, 0, chunkCount+1)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows = append(rows, tailEventRow(
		1,
		base,
		"benchmark",
		event.TypeSessionLifecycle,
		"created",
		"",
	))
	offset := uint64(0)
	for index := 0; index < chunkCount; index++ {
		data := append([]byte(nil), chunk...)
		rows = append(rows, tailOutputRow(
			uint64(index+2),
			base.Add(time.Duration(index+1)*time.Microsecond),
			"benchmark",
			offset,
			data,
		))
		offset += uint64(len(data))
	}
	if offset != frameBenchmarkBytes {
		b.Fatalf("frame benchmark bytes = %d, want %d", offset, frameBenchmarkBytes)
	}
	lastSequence, err := st.AppendEvents(context.Background(), 0, rows)
	if err != nil {
		b.Fatalf("append frame benchmark fixture: %v", err)
	}
	return st, lastSequence
}

func frameBenchmarkChunk() []byte {
	pattern := []byte(
		"\x1b[2J\x1b[Hframe benchmark\n" +
			"\x1b[2;1H界\x1b]0;drove\x07\x1b[?25l\x1b[?25h",
	)
	chunk := bytes.Repeat(
		pattern,
		event.MaxOutputChunkBytes/len(pattern)+1,
	)
	return chunk[:event.MaxOutputChunkBytes]
}

func randomFrameBenchmarkOffsets(count int) []OutputOffset {
	random := rand.New(rand.NewSource(19))
	offsets := make([]OutputOffset, count)
	for index := range offsets {
		offsets[index] = OutputOffset(
			random.Int63n(frameBenchmarkBytes-1) + 1,
		)
	}
	return offsets
}

func reportFrameBenchmarkMetrics(
	b *testing.B,
	durations []time.Duration,
	replayed uint64,
) {
	b.Helper()
	if len(durations) == 0 {
		return
	}
	slices.Sort(durations)
	p50 := nearestRankDuration(durations, 50)
	p95 := nearestRankDuration(durations, 95)
	b.ReportMetric(float64(p50)/float64(time.Millisecond), "p50-ms")
	b.ReportMetric(float64(p95)/float64(time.Millisecond), "p95-ms")
	b.ReportMetric(
		float64(replayed)/float64(len(durations))/(1<<20),
		"replayed-MiB/op",
	)
}

func nearestRankDuration(samples []time.Duration, percentile int) time.Duration {
	index := (len(samples)*percentile+99)/100 - 1
	return samples[index]
}
