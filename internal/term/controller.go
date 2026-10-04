package term

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

const (
	maxTerminalDimension = 1<<16 - 1
	replyBufferSize      = 4096

	// MaxViewRows bounds the number of terminal rows in an external view.
	MaxViewRows = 12
	// MaxViewCellsPerRow bounds each row in an external view.
	MaxViewCellsPerRow = 160
	// MaxViewBytes bounds the UTF-8 bytes in an external view.
	MaxViewBytes = 4 * 1024
)

// ErrClosed reports an operation on a closed Controller.
var ErrClosed = errors.New("terminal controller is closed")

// Size is a validated terminal size.
type Size struct {
	rows    int
	columns int
}

// NewSize validates and constructs a terminal size.
func NewSize(rows, columns int) (Size, error) {
	size := Size{rows: rows, columns: columns}
	if err := size.validate(); err != nil {
		return Size{}, err
	}
	return size, nil
}

// Rows returns the terminal height.
func (s Size) Rows() int {
	return s.rows
}

// Columns returns the terminal width.
func (s Size) Columns() int {
	return s.columns
}

func (s Size) validate() error {
	if s.rows <= 0 || s.rows > maxTerminalDimension {
		return fmt.Errorf("terminal rows must be between 1 and %d", maxTerminalDimension)
	}
	if s.columns <= 0 || s.columns > maxTerminalDimension {
		return fmt.Errorf("terminal columns must be between 1 and %d", maxTerminalDimension)
	}
	return nil
}

// CommittedChunk is a copied terminal-output batch that is already durable.
type CommittedChunk struct {
	bytes        []byte
	outputOffset uint64
	lastSeq      uint64
	committedAt  time.Time
}

// NewCommittedChunk validates metadata and copies the committed bytes.
func NewCommittedChunk(
	data []byte,
	outputOffset uint64,
	lastSeq uint64,
	committedAt time.Time,
) (CommittedChunk, error) {
	if len(data) == 0 {
		return CommittedChunk{}, errors.New("committed terminal chunk is empty")
	}
	if outputOffset < uint64(len(data)) {
		return CommittedChunk{}, fmt.Errorf(
			"output offset %d is smaller than chunk length %d",
			outputOffset,
			len(data),
		)
	}
	if lastSeq == 0 {
		return CommittedChunk{}, errors.New("last output sequence must be positive")
	}
	if committedAt.IsZero() {
		return CommittedChunk{}, errors.New("commit time is required")
	}
	return CommittedChunk{
		bytes:        append([]byte(nil), data...),
		outputOffset: outputOffset,
		lastSeq:      lastSeq,
		committedAt:  committedAt,
	}, nil
}

// Bytes returns a copy of the committed terminal bytes.
func (c CommittedChunk) Bytes() []byte {
	return append([]byte(nil), c.bytes...)
}

// OutputOffset returns the exclusive output offset represented by the chunk.
func (c CommittedChunk) OutputOffset() uint64 {
	return c.outputOffset
}

// LastSeq returns the final durable output event sequence in the chunk.
func (c CommittedChunk) LastSeq() uint64 {
	return c.lastSeq
}

// CommittedAt returns the chunk commit time.
func (c CommittedChunk) CommittedAt() time.Time {
	return c.committedAt
}

type snapshotCell struct {
	content string
	width   int
}

// Snapshot is an immutable normalized copy of the visible terminal cells.
type Snapshot struct {
	size Size
	rows [][]snapshotCell
}

// Size returns the dimensions captured by the snapshot.
func (s Snapshot) Size() Size {
	return s.size
}

// Rows returns the number of visible rows in the snapshot.
func (s Snapshot) Rows() int {
	return len(s.rows)
}

// Row returns one visible row without trailing blank cells.
func (s Snapshot) Row(index int) (string, bool) {
	if index < 0 || index >= len(s.rows) {
		return "", false
	}
	text, _, _ := renderRow(s.rows[index], len(s.rows[index]), -1)
	return text, true
}

// ViewOptions is a validated set of privacy bounds for Snapshot.View.
type ViewOptions struct {
	bottomRows     int
	maxCellsPerRow int
	maxBytes       int
}

// NewViewOptions constructs snapshot view bounds within the package limits.
func NewViewOptions(bottomRows, maxCellsPerRow, maxBytes int) (ViewOptions, error) {
	options := ViewOptions{
		bottomRows:     bottomRows,
		maxCellsPerRow: maxCellsPerRow,
		maxBytes:       maxBytes,
	}
	if err := options.validate(); err != nil {
		return ViewOptions{}, err
	}
	return options, nil
}

// DefaultViewOptions returns the maximum permitted external snapshot bounds.
func DefaultViewOptions() ViewOptions {
	return ViewOptions{
		bottomRows:     MaxViewRows,
		maxCellsPerRow: MaxViewCellsPerRow,
		maxBytes:       MaxViewBytes,
	}
}

func (o ViewOptions) validate() error {
	if o.bottomRows <= 0 || o.bottomRows > MaxViewRows {
		return fmt.Errorf("bottom rows must be between 1 and %d", MaxViewRows)
	}
	if o.maxCellsPerRow <= 0 || o.maxCellsPerRow > MaxViewCellsPerRow {
		return fmt.Errorf("cells per row must be between 1 and %d", MaxViewCellsPerRow)
	}
	if o.maxBytes <= 0 || o.maxBytes > MaxViewBytes {
		return fmt.Errorf("view bytes must be between 1 and %d", MaxViewBytes)
	}
	return nil
}

// SnapshotView is a copied, bounded text view of a Snapshot.
type SnapshotView struct {
	rows      []string
	truncated bool
}

// Rows returns a copy of the bounded rows.
func (v SnapshotView) Rows() []string {
	return append([]string(nil), v.rows...)
}

// Truncated reports whether row, cell, or byte bounds omitted visible cells.
func (v SnapshotView) Truncated() bool {
	return v.truncated
}

// View returns the bottom visible rows under validated privacy bounds.
func (s Snapshot) View(options ViewOptions) (SnapshotView, error) {
	if err := options.validate(); err != nil {
		return SnapshotView{}, err
	}

	end := len(s.rows)
	for end > 0 && rowContentEnd(s.rows[end-1]) == 0 {
		end--
	}
	if end == 0 {
		return SnapshotView{}, nil
	}

	start := max(0, end-options.bottomRows)
	truncated := start > 0
	remaining := options.maxBytes
	reversed := make([]string, 0, end-start)
	for index := end - 1; index >= start; index-- {
		separatorBytes := 0
		if len(reversed) > 0 {
			separatorBytes = 1
		}
		if remaining <= separatorBytes {
			truncated = true
			break
		}

		row, cellsClipped, bytesClipped := renderRow(
			s.rows[index],
			options.maxCellsPerRow,
			remaining-separatorBytes,
		)
		reversed = append(reversed, row)
		remaining -= separatorBytes + len(row)
		truncated = truncated || cellsClipped || bytesClipped
		if bytesClipped {
			break
		}
	}

	rows := make([]string, len(reversed))
	for index := range reversed {
		rows[len(reversed)-1-index] = reversed[index]
	}
	return SnapshotView{rows: rows, truncated: truncated}, nil
}

func renderRow(cells []snapshotCell, maxCells, maxBytes int) (string, bool, bool) {
	contentEnd := rowContentEnd(cells)
	cellEnd := min(contentEnd, maxCells)
	cellsClipped := contentEnd > maxCells
	if cellEnd == 0 {
		return "", cellsClipped, false
	}

	var builder strings.Builder
	continuationUntil := 0
	for column := 0; column < cellEnd; column++ {
		if column < continuationUntil {
			continue
		}

		cell := cells[column]
		width := max(1, cell.width)
		if column+width > cellEnd {
			return strings.TrimRight(builder.String(), " "), true, false
		}
		content := cell.content
		if content == "" {
			content = " "
		}
		if maxBytes >= 0 && builder.Len()+len(content) > maxBytes {
			return strings.TrimRight(builder.String(), " "), cellsClipped, true
		}
		builder.WriteString(content)
		continuationUntil = column + width
	}
	return strings.TrimRight(builder.String(), " "), cellsClipped, false
}

func rowContentEnd(cells []snapshotCell) int {
	end := 0
	for column, cell := range cells {
		if cell.content == "" || cell.content == " " {
			continue
		}
		end = max(end, column+max(1, cell.width))
	}
	return min(end, len(cells))
}

// ReplySink writes one complete terminal reply frame.
type ReplySink func([]byte) error

// Controller owns terminal emulation and its reply pump.
type Controller struct {
	mu          sync.Mutex
	emulator    *vt.Emulator
	replyWriter io.WriteCloser
	replySink   ReplySink
	replyErrors chan error
	pumpDone    chan struct{}
	closeOnce   sync.Once
	closeErr    error
	closed      bool
}

// NewController constructs a terminal and starts its reply pump.
func NewController(size Size, replySink ReplySink) (*Controller, error) {
	if err := size.validate(); err != nil {
		return nil, fmt.Errorf("create terminal controller: %w", err)
	}
	if replySink == nil {
		return nil, errors.New("create terminal controller: reply sink is required")
	}

	emulator := vt.NewEmulator(size.Columns(), size.Rows())
	replyWriter, ok := emulator.InputPipe().(io.WriteCloser)
	if !ok {
		_ = emulator.Close()
		return nil, errors.New("create terminal controller: x/vt reply pipe is not closable")
	}
	emulator.RegisterCsiHandler(ansi.Command('?', 0, 'u'), func(params ansi.Params) bool {
		if len(params) != 0 {
			return false
		}
		_, _ = io.WriteString(replyWriter, "\x1b[?0u")
		return true
	})

	controller := &Controller{
		emulator:    emulator,
		replyWriter: replyWriter,
		replySink:   replySink,
		replyErrors: make(chan error, 1),
		pumpDone:    make(chan struct{}),
	}
	ready := make(chan struct{})
	go controller.pumpReplies(ready)
	<-ready
	return controller, nil
}

// Errors returns bounded asynchronous reply-sink failures.
func (c *Controller) Errors() <-chan error {
	return c.replyErrors
}

// Write applies committed output bytes to the terminal.
func (c *Controller) Write(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	written, err := c.emulator.Write(data)
	if err != nil {
		return fmt.Errorf("write terminal output: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("write terminal output: %w", io.ErrShortWrite)
	}
	return nil
}

// Resize changes the emulator dimensions.
func (c *Controller) Resize(size Size) error {
	if err := size.validate(); err != nil {
		return fmt.Errorf("resize terminal: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	c.emulator.Resize(size.Columns(), size.Rows())
	return nil
}

// Snapshot copies the current visible screen without terminal metadata.
func (c *Controller) Snapshot() (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return Snapshot{}, ErrClosed
	}

	size, err := NewSize(c.emulator.Height(), c.emulator.Width())
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot terminal: %w", err)
	}
	rows := make([][]snapshotCell, size.Rows())
	for row := range rows {
		rows[row] = make([]snapshotCell, size.Columns())
		for column := range rows[row] {
			cell := c.emulator.CellAt(column, row)
			if cell == nil {
				rows[row][column] = snapshotCell{content: " ", width: 1}
				continue
			}
			rows[row][column] = snapshotCell{
				content: cell.Content,
				width:   cell.Width,
			}
		}
	}
	return Snapshot{size: size, rows: rows}, nil
}

// Close stops the reply pump and closes the emulator. It is idempotent.
func (c *Controller) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		writerErr := c.replyWriter.Close()
		c.mu.Unlock()

		<-c.pumpDone

		c.mu.Lock()
		emulatorErr := c.emulator.Close()
		c.mu.Unlock()
		switch {
		case writerErr != nil:
			c.closeErr = fmt.Errorf("close terminal reply pipe: %w", writerErr)
		case emulatorErr != nil:
			c.closeErr = fmt.Errorf("close terminal emulator: %w", emulatorErr)
		}
	})
	return c.closeErr
}

func (c *Controller) pumpReplies(ready chan<- struct{}) {
	defer close(c.pumpDone)
	defer close(c.replyErrors)
	close(ready)

	buffer := make([]byte, replyBufferSize)
	for {
		count, err := c.emulator.Read(buffer)
		if count > 0 {
			frame := append([]byte(nil), buffer[:count]...)
			if sinkErr := c.replySink(frame); sinkErr != nil {
				select {
				case c.replyErrors <- fmt.Errorf("write terminal reply: %w", sinkErr):
				default:
				}
			}
		}
		if err != nil {
			return
		}
	}
}
