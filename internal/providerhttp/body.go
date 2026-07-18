// Package providerhttp contains the HTTP response limits shared by the LLM
// and Agent clients. Provider endpoints are user-configurable and their
// responses must therefore be treated as untrusted input.
package providerhttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	// MaxCompletionResponseBytes is deliberately much larger than a normal
	// non-streaming chat completion while still putting a hard ceiling on the
	// allocation performed before JSON decoding.
	MaxCompletionResponseBytes int64 = 8 * 1024 * 1024

	// MaxModelListResponseBytes allows tens of thousands of normal model IDs.
	MaxModelListResponseBytes int64 = 2 * 1024 * 1024

	// MaxStreamResponseBytes bounds the whole SSE response, not just one event.
	// Streaming still avoids retaining that data in memory.
	MaxStreamResponseBytes int64 = 32 * 1024 * 1024

	// MaxErrorResponseBytes limits provider-controlled text included in errors.
	MaxErrorResponseBytes int64 = 8 * 1024
)

// ErrResponseTooLarge identifies a response rejected by an acquisition-time
// byte budget. Callers can use errors.Is while retaining the concrete limit in
// the returned error message.
var ErrResponseTooLarge = errors.New("provider response body is too large")

// ResponseTooLargeError reports the byte ceiling that was exceeded.
type ResponseTooLargeError struct {
	Limit int64
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("provider response body exceeds the %s limit", formatBytes(e.Limit))
}

func (e *ResponseTooLargeError) Unwrap() error { return ErrResponseTooLarge }

// NewBoundedReader returns a reader that exposes at most limit bytes and then
// probes for one additional byte. That probe is important: io.LimitReader by
// itself makes a too-large body indistinguishable from a body exactly at the
// limit. A declared Content-Length over the limit is rejected before reading.
func NewBoundedReader(resp *http.Response, limit int64) (io.Reader, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("provider response has no body")
	}
	if limit < 0 {
		return nil, errors.New("provider response limit must not be negative")
	}
	if resp.ContentLength > limit {
		return nil, &ResponseTooLargeError{Limit: limit}
	}
	return &boundedReader{reader: resp.Body, remaining: limit, limit: limit}, nil
}

// ReadAll reads a complete response without ever acquiring more than limit
// bytes. A declared oversize is rejected before reading; when a chunked or
// unknown-length body crosses the limit, ReadAll returns the bounded prefix
// together with ErrResponseTooLarge so diagnostic code can record safe
// metadata without retrying or reading the rest of the body.
func ReadAll(resp *http.Response, limit int64) ([]byte, error) {
	r, err := NewBoundedReader(resp, limit)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

type boundedReader struct {
	reader    io.Reader
	remaining int64
	limit     int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining > 0 {
		if int64(len(p)) > r.remaining {
			p = p[:r.remaining]
		}
		n, err := r.reader.Read(p)
		r.remaining -= int64(n)
		return n, err
	}

	// The allowed prefix has been consumed. Probe without exposing the extra
	// byte to the caller so no decoder or SSE callback can process it.
	var probe [1]byte
	n, err := r.reader.Read(probe[:])
	if n > 0 {
		return 0, &ResponseTooLargeError{Limit: r.limit}
	}
	return 0, err
}

func formatBytes(n int64) string {
	const mib = 1024 * 1024
	const kib = 1024
	switch {
	case n > 0 && n%mib == 0:
		return fmt.Sprintf("%d MiB", n/mib)
	case n > 0 && n%kib == 0:
		return fmt.Sprintf("%d KiB", n/kib)
	default:
		return fmt.Sprintf("%d-byte", n)
	}
}
