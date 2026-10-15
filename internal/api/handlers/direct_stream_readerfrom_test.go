// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestDirectFileResponseWriterReadFrom(t *testing.T) {
	t.Run("delegates to the downstream ReaderFrom without writing through the wrapper", func(t *testing.T) {
		readFromErr := errors.New("downstream reader-from failure")
		downstream := &readerFromResponseWriter{readFromErr: readFromErr}
		writer := &directFileResponseWriter{ResponseWriter: downstream}

		gotBytes, gotErr := writer.ReadFrom(bytes.NewBufferString("streamed"))

		if gotBytes != int64(len("streamed")) {
			t.Errorf("ReadFrom() copied %d bytes, want %d", gotBytes, len("streamed"))
		}
		if !errors.Is(gotErr, readFromErr) {
			t.Errorf("ReadFrom() error = %v, want downstream error %v", gotErr, readFromErr)
		}
		if downstream.readFromCalls != 1 {
			t.Errorf("downstream ReadFrom() calls = %d, want 1", downstream.readFromCalls)
		}
		if downstream.writeCalls != 0 {
			t.Errorf("downstream Write() calls = %d, want 0; ReaderFrom streaming must not pass bytes through wrapper Write", downstream.writeCalls)
		}
		if got := downstream.body.String(); got != "streamed" {
			t.Errorf("downstream streamed body = %q, want %q", got, "streamed")
		}
	})

	t.Run("copies through a writer without ReaderFrom", func(t *testing.T) {
		downstream := &responseWriter{}
		writer := &directFileResponseWriter{ResponseWriter: downstream}

		gotBytes, gotErr := writer.ReadFrom(bytes.NewBufferString("fallback"))

		if gotBytes != int64(len("fallback")) {
			t.Errorf("ReadFrom() copied %d bytes, want %d", gotBytes, len("fallback"))
		}
		if gotErr != nil {
			t.Errorf("ReadFrom() error = %v, want nil", gotErr)
		}
		if got := downstream.body.String(); got != "fallback" {
			t.Errorf("fallback downstream body = %q, want %q", got, "fallback")
		}
	})

	t.Run("consumes discarded range-response bodies without downstream writes", func(t *testing.T) {
		downstream := &readerFromResponseWriter{}
		writer := &directFileResponseWriter{ResponseWriter: downstream, discardBody: true}
		source := bytes.NewReader([]byte("discarded"))

		gotBytes, gotErr := writer.ReadFrom(source)

		if gotBytes != int64(len("discarded")) {
			t.Errorf("ReadFrom() copied %d bytes, want %d", gotBytes, len("discarded"))
		}
		if gotErr != nil {
			t.Errorf("ReadFrom() error = %v, want nil", gotErr)
		}
		if source.Len() != 0 {
			t.Errorf("source has %d unread bytes, want 0", source.Len())
		}
		if downstream.readFromCalls != 0 {
			t.Errorf("downstream ReadFrom() calls = %d, want 0 while discarding", downstream.readFromCalls)
		}
		if downstream.writeCalls != 0 {
			t.Errorf("downstream Write() calls = %d, want 0 while discarding", downstream.writeCalls)
		}
		if got := downstream.body.String(); got != "" {
			t.Errorf("discarded downstream body = %q, want empty", got)
		}
	})
}

func TestDirectFileResponseWriterUnwrapsDownstreamWriter(t *testing.T) {
	downstream := &responseWriter{}
	writer := &directFileResponseWriter{ResponseWriter: downstream}

	if got := writer.Unwrap(); got != downstream {
		t.Errorf("Unwrap() = %T %p, want original downstream writer %T %p", got, got, downstream, downstream)
	}
}

type responseWriter struct {
	header     http.Header
	body       bytes.Buffer
	writeCalls int
}

func (w *responseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *responseWriter) Write(value []byte) (int, error) {
	w.writeCalls++
	return w.body.Write(value)
}

func (*responseWriter) WriteHeader(int) {}

type readerFromResponseWriter struct {
	responseWriter
	readFromCalls int
	readFromErr   error
}

func (w *readerFromResponseWriter) ReadFrom(source io.Reader) (int64, error) {
	w.readFromCalls++
	n, err := io.Copy(&w.body, source)
	if err != nil {
		return n, err
	}
	return n, w.readFromErr
}
