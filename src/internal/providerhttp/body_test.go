package providerhttp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadAllAcceptsBodyAtLimit(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader("12345")),
		ContentLength: 5,
	}
	got, err := ReadAll(resp, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "12345" {
		t.Fatalf("body = %q, want %q", got, "12345")
	}
}

func TestReadAllRejectsDeclaredOversizeBeforeReading(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader("not read")),
		ContentLength: 6,
	}
	got, err := ReadAll(resp, 5)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
	if got != nil {
		t.Fatalf("body = %q, want nil", got)
	}
}

func TestReadAllRejectsOversizedChunkedResponse(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // prevents net/http from synthesizing Content-Length
		}
		_, _ = io.WriteString(w, "123456")
	}))
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ContentLength != -1 {
		t.Fatalf("test response Content-Length = %d, want chunked/unknown", resp.ContentLength)
	}
	got, err := ReadAll(resp, 5)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
	if string(got) != "12345" {
		t.Fatalf("bounded prefix = %q, want %q", got, "12345")
	}
}
