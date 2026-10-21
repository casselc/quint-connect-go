package qobs

import (
	"bufio"
	"context"
	"io"
	"os"
	"sync"

	"github.com/casselc/quint-connect-go/qtrace"
)

// JSONLSink writes each step as one line of the native step log: a flat
// JSON object with the same keys as the OTel attributes (see qtrace), no
// OTel involved. `quintgo validate` reads it like OTLP/JSON.
type JSONLSink struct {
	mu  sync.Mutex
	w   io.Writer
	buf *bufio.Writer // FileSink only
	c   io.Closer
	err error
}

// NewJSONLSink writes the step log to w.
func NewJSONLSink(w io.Writer) *JSONLSink { return &JSONLSink{w: w} }

// FileSink creates (truncating) a buffered step log file. Close (or Flush)
// it when done: steps still in the buffer are lost if the process dies.
func FileSink(path string) (*JSONLSink, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	b := bufio.NewWriterSize(f, 64*1024)
	return &JSONLSink{w: b, buf: b, c: f}, nil
}

// DurableFileSink creates (truncating) a step log file written one step
// at a time, with no buffer in the process: a step is in the file (the
// OS's, at least) when Record returns, so a process that is killed loses
// none. Use it where crashes are part of what is tested; FileSink is
// faster. Steps lost at the end of a log cannot be detected (no later
// sequence number reveals the gap), which is why this matters.
func DurableFileSink(path string) (*JSONLSink, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &JSONLSink{w: f, c: f}, nil
}

// Flush writes buffered steps to the file.
func (j *JSONLSink) Flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.buf != nil {
		if err := j.buf.Flush(); err != nil && j.err == nil {
			j.err = err
		}
	}
	return j.err
}

func (j *JSONLSink) Record(_ context.Context, s *qtrace.Step) {
	b, err := s.MarshalLine()
	j.mu.Lock()
	defer j.mu.Unlock()
	if err == nil {
		_, err = j.w.Write(b)
	}
	if err != nil && j.err == nil {
		j.err = err
	}
}

// Err is the first write error, if any.
func (j *JSONLSink) Err() error { j.mu.Lock(); defer j.mu.Unlock(); return j.err }

// Close closes the file of a FileSink and returns the first error.
func (j *JSONLSink) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.buf != nil {
		if err := j.buf.Flush(); err != nil && j.err == nil {
			j.err = err
		}
		j.buf = nil
	}
	if j.c != nil {
		if err := j.c.Close(); err != nil && j.err == nil {
			j.err = err
		}
		j.c = nil
	}
	return j.err
}
