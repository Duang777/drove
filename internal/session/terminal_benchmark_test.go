package session

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/term"
)

const (
	terminalBenchmarkActors     = 32
	terminalBenchmarkBytes      = 1 << 20
	terminalBenchmarkChunkBytes = 16 << 10
	terminalBenchmarkRate       = 1 << 20
)

func BenchmarkTerminalActor32(b *testing.B) {
	size, err := initialTerminalSize()
	if err != nil {
		b.Fatalf("create initial terminal size: %v", err)
	}
	streams := make([][]byte, terminalBenchmarkActors)
	markers := make([]string, terminalBenchmarkActors)
	for index := range terminalBenchmarkActors {
		streams[index], markers[index] = terminalBenchmarkStream(index)
	}

	b.ReportAllocs()
	b.SetBytes(terminalBenchmarkActors * terminalBenchmarkBytes)
	b.ResetTimer()

	var totalBytes int64
	peakGoroutines := runtime.NumGoroutine()
	maxInboxDepth := 0
	backpressureEngaged := false

	for range b.N {
		actors := make([]*terminalActor, terminalBenchmarkActors)
		for index := range actors {
			classifier, err := adapter.NewRegistry().
				For("generic").
				NewScreenClassifier()
			if err != nil {
				b.Fatalf("create classifier %d: %v", index, err)
			}
			actor, err := newTerminalActor(
				size,
				&terminalBenchmarkProcess{},
				classifier,
				nil,
				nil,
				"generic",
				systemObservationClock{},
				nil,
			)
			if err != nil {
				b.Fatalf("create actor %d: %v", index, err)
			}
			actors[index] = actor
		}

		var runBytes atomic.Int64
		var monitor terminalBenchmarkMonitor
		monitor.start(actors)
		startedAt := time.Now()
		errors := make(chan error, terminalBenchmarkActors)
		var workers sync.WaitGroup
		for index, actor := range actors {
			workers.Add(1)
			go func() {
				defer workers.Done()
				stream := streams[index]
				var offset uint64
				var sequence uint64
				for chunkStart := 0; chunkStart < len(stream); chunkStart += terminalBenchmarkChunkBytes {
					chunkEnd := min(
						len(stream),
						chunkStart+terminalBenchmarkChunkBytes,
					)
					data := stream[chunkStart:chunkEnd]
					offset += uint64(len(data))
					sequence++
					chunk, err := term.NewCommittedChunk(
						data,
						offset,
						sequence,
						time.Now().UTC(),
					)
					if err != nil {
						errors <- fmt.Errorf("actor %d create chunk: %w", index, err)
						return
					}
					if _, err := actor.FeedCommitted(context.Background(), chunk); err != nil {
						errors <- fmt.Errorf("actor %d feed chunk: %w", index, err)
						return
					}
					runBytes.Add(int64(len(data)))

					target := startedAt.Add(
						time.Duration(offset) * time.Second / terminalBenchmarkRate,
					)
					if wait := time.Until(target); wait > 0 {
						time.Sleep(wait)
					}
				}
				if err := actor.EndOutput(context.Background(), offset); err != nil {
					errors <- fmt.Errorf("actor %d end output: %w", index, err)
				}
			}()
		}
		workers.Wait()
		close(errors)
		for err := range errors {
			b.Error(err)
		}
		if b.Failed() {
			for _, actor := range actors {
				_ = actor.Close()
			}
			b.FailNow()
		}

		wantBytes := int64(terminalBenchmarkActors * terminalBenchmarkBytes)
		if got := runBytes.Load(); got != wantBytes {
			b.Fatalf("committed bytes = %d, want %d", got, wantBytes)
		}
		totalBytes += runBytes.Load()
		for index, actor := range actors {
			snapshot, available := actor.Snapshot()
			if !available {
				b.Fatalf("actor %d final snapshot is unavailable", index)
			}
			row, ok := snapshot.Row(0)
			if !ok || row != markers[index] {
				b.Fatalf("actor %d final row = %q, want %q", index, row, markers[index])
			}
			if err := actor.Close(); err != nil {
				b.Fatalf("close actor %d: %v", index, err)
			}
		}
		sample := monitor.stop()
		peakGoroutines = max(peakGoroutines, sample.peakGoroutines)
		maxInboxDepth = max(maxInboxDepth, sample.maxInboxDepth)
		backpressureEngaged = backpressureEngaged || sample.backpressureEngaged
	}

	elapsed := b.Elapsed()
	if elapsed > 0 {
		throughput := float64(totalBytes) / elapsed.Seconds() / (1 << 20)
		b.ReportMetric(throughput, "aggregate-MiB/s")
	}
	b.ReportMetric(float64(totalBytes)/float64(b.N), "committed-bytes/op")
	b.ReportMetric(float64(peakGoroutines), "peak-goroutines")
	b.ReportMetric(float64(maxInboxDepth), "max-inbox-depth")
	if backpressureEngaged {
		b.ReportMetric(1, "backpressure-engaged")
	} else {
		b.ReportMetric(0, "backpressure-engaged")
	}
}

func terminalBenchmarkStream(index int) ([]byte, string) {
	marker := fmt.Sprintf("terminal-benchmark-%02d-final", index)
	final := []byte("\x1b[2J\x1b[H" + marker)
	control := []byte(fmt.Sprintf(
		"\x1b[2J\x1b[Hsession-%02d working\x1b[2;1Hframe output"+
			"\x1b[2;1Hoverwritten output\x1b[4;20H界\x1b[?25l\x1b[?25h",
		index,
	))
	pattern := make([]byte, 4<<10)
	copy(pattern, control)
	for position := len(control); position < len(pattern); position++ {
		pattern[position] = 'x'
	}
	prefixBytes := terminalBenchmarkBytes - len(final)
	stream := make([]byte, 0, terminalBenchmarkBytes)
	for len(stream)+len(pattern) <= prefixBytes {
		stream = append(stream, pattern...)
	}
	stream = append(stream, strings.Repeat("x", prefixBytes-len(stream))...)
	stream = append(stream, final...)
	return stream, marker
}

type terminalBenchmarkProcess struct{}

func (*terminalBenchmarkProcess) Write(frame []byte) (int, error) {
	return len(frame), nil
}

func (*terminalBenchmarkProcess) Resize(uint16, uint16) error {
	return nil
}

type terminalBenchmarkSample struct {
	peakGoroutines      int
	maxInboxDepth       int
	backpressureEngaged bool
}

type terminalBenchmarkMonitor struct {
	stopChannel chan struct{}
	done        chan terminalBenchmarkSample
}

func (m *terminalBenchmarkMonitor) start(actors []*terminalActor) {
	m.stopChannel = make(chan struct{})
	m.done = make(chan terminalBenchmarkSample, 1)
	go func() {
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		sample := terminalBenchmarkSample{
			peakGoroutines: runtime.NumGoroutine(),
		}
		for {
			select {
			case <-ticker.C:
				sample.peakGoroutines = max(
					sample.peakGoroutines,
					runtime.NumGoroutine(),
				)
				for _, actor := range actors {
					depth := len(actor.requests)
					sample.maxInboxDepth = max(sample.maxInboxDepth, depth)
					if depth == cap(actor.requests) {
						sample.backpressureEngaged = true
					}
				}
			case <-m.stopChannel:
				m.done <- sample
				return
			}
		}
	}()
}

func (m *terminalBenchmarkMonitor) stop() terminalBenchmarkSample {
	close(m.stopChannel)
	return <-m.done
}
