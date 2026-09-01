package main

import (
	"bufio"
	"bytes"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTrackingWriter() (*trackingWriter, *bytes.Buffer, *atomic.Int64) {
	buf := &bytes.Buffer{}
	var total atomic.Int64
	tw := &trackingWriter{ResponseWriter: &stubResponseWriter{buf: buf}, written: &total}
	return tw, buf, &total
}

// stubResponseWriter is a minimal http.ResponseWriter for testing
// trackingWriter in isolation.
type stubResponseWriter struct {
	buf      *bytes.Buffer
	status   int
	flushed  bool
	hijacked bool
}

func (s *stubResponseWriter) Header() http.Header         { return http.Header{} }
func (s *stubResponseWriter) WriteHeader(code int)        { s.status = code }
func (s *stubResponseWriter) Write(p []byte) (int, error) { return s.buf.Write(p) }
func (s *stubResponseWriter) Flush()                      { s.flushed = true }
func (s *stubResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	s.hijacked = true
	return nil, nil, errors.New("not supported here")
}

func TestTrackingWriterWrite(t *testing.T) {
	tw, buf, total := newTrackingWriter()

	for _, chunk := range []string{"hello", " ", "world"} {
		n, err := tw.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != len(chunk) {
			t.Fatalf("wrote %d bytes, want %d", n, len(chunk))
		}
	}

	if got := buf.String(); got != "hello world" {
		t.Fatalf("buffered content = %q, want %q", got, "hello world")
	}
	if got := total.Load(); got != int64(len("hello world")) {
		t.Fatalf("total = %d, want %d", got, len("hello world"))
	}
}

func TestTrackingWriterFlush(t *testing.T) {
	tw, buf, _ := newTrackingWriter()
	tw.Flush()
	_ = buf
	underlying := tw.ResponseWriter.(*stubResponseWriter)
	if !underlying.flushed {
		t.Fatal("expected Flush to reach the underlying writer")
	}
}

func TestTrackingWriterHijackPassesThrough(t *testing.T) {
	tw, _, _ := newTrackingWriter()
	_, _, err := tw.Hijack()
	if err == nil || !strings.Contains(err.Error(), "not supported here") {
		t.Fatalf("Hijack error = %v, want underlying error", err)
	}
}

func TestTrackingWriterReadFrom(t *testing.T) {
	tw, buf, total := newTrackingWriter()

	src := strings.NewReader("0123456789")
	n, err := tw.ReadFrom(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 10 {
		t.Fatalf("read %d bytes, want 10", n)
	}
	if got := buf.String(); got != "0123456789" {
		t.Fatalf("buffered content = %q", got)
	}
	if got := total.Load(); got != 10 {
		t.Fatalf("total = %d, want 10", got)
	}
}

func TestTrackingWriterPushNotSupported(t *testing.T) {
	tw, _, _ := newTrackingWriter()
	if err := tw.Push("/x", nil); err != http.ErrNotSupported {
		t.Fatalf("Push error = %v, want http.ErrNotSupported", err)
	}
}

func TestTrackingWriterSuccess(t *testing.T) {
	tw, _, _ := newTrackingWriter()
	// Defaults to success when no explicit status is written.
	if !tw.success() {
		t.Fatal("expected success()=true before WriteHeader")
	}
	tw.WriteHeader(http.StatusOK)
	if !tw.success() {
		t.Fatal("expected success()=true for 200")
	}
	if tw.status != http.StatusOK || !tw.wroteHdr {
		t.Fatalf("status=%d wroteHdr=%v", tw.status, tw.wroteHdr)
	}

	tw2, _, _ := newTrackingWriter()
	tw2.WriteHeader(http.StatusNotFound)
	if tw2.success() {
		t.Fatal("expected success()=false for 404")
	}
}

// --- fileHandler tests ---

// setupTempRoot creates a temp dir with a known file and returns its path.
func setupTempRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	content := []byte("the quick brown fox jumps over the lazy dog")
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "nested.txt"), []byte("nested-content"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	return root
}

// syncBuffer is a concurrency-safe bytes.Buffer for capturing log output,
// since the handler may write logs from an internal goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// captureLogs creates a thread-safe buffered logger.
func captureLogs() (*syncBuffer, *log.Logger) {
	buf := &syncBuffer{}
	return buf, log.New(buf, "", 0)
}

func TestFileHandlerServesFileContent(t *testing.T) {
	root := setupTempRoot(t)
	logBuf, logger := captureLogs()
	h := fileHandler(root, logger, 0)

	req := httptest.NewRequest(http.MethodGet, "/hello.txt", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	want := "the quick brown fox jumps over the lazy dog"
	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}

	// A completed download should have been logged.
	logs := logBuf.String()
	if !strings.Contains(logs, "download complete") {
		t.Fatalf("expected download complete log, got: %q", logs)
	}
	if !strings.Contains(logs, "/hello.txt") {
		t.Fatalf("expected path in log, got: %q", logs)
	}
	if !strings.Contains(logs, "avg speed") {
		t.Fatalf("expected avg speed in log, got: %q", logs)
	}
}

func TestFileHandlerServesNestedFile(t *testing.T) {
	root := setupTempRoot(t)
	logBuf, logger := captureLogs()
	h := fileHandler(root, logger, 0)

	req := httptest.NewRequest(http.MethodGet, "/sub/nested.txt", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "nested-content" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "nested-content")
	}
	if logBuf.Len() == 0 {
		t.Fatal("expected log output")
	}
}

func TestFileHandlerMissingFile(t *testing.T) {
	root := setupTempRoot(t)
	logBuf, logger := captureLogs()
	h := fileHandler(root, logger, 0)

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist.txt", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	// A 404 transferred no bytes, so it should not produce a
	// "download complete" report.
	if strings.Contains(logBuf.String(), "download complete") {
		t.Fatalf("expected no download complete log for 404, got: %q", logBuf.String())
	}
}

func TestFileHandlerNonGETPassesThrough(t *testing.T) {
	root := setupTempRoot(t)
	logBuf, logger := captureLogs()
	h := fileHandler(root, logger, 0)

	// Non-GET requests are passed through to the file server (which serves
	// the file) but must NOT produce speed-tracking logs.
	req := httptest.NewRequest(http.MethodPost, "/hello.txt", strings.NewReader("data"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "the quick brown fox jumps over the lazy dog" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if logBuf.Len() != 0 {
		t.Fatalf("expected no logs for non-GET, got: %q", logBuf.String())
	}
}

// throttledResponseWriter simulates a slow client by sleeping on each write,
// so the periodic reporting ticker (with a tiny interval) deterministically
// fires during a transfer.
type throttledResponseWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (t *throttledResponseWriter) Header() http.Header {
	if t.header == nil {
		t.header = http.Header{}
	}
	return t.header
}
func (t *throttledResponseWriter) WriteHeader(code int) { t.status = code }
func (t *throttledResponseWriter) Write(p []byte) (int, error) {
	time.Sleep(time.Millisecond)
	return t.buf.Write(p)
}

func TestFileHandlerReportsIntervalSpeed(t *testing.T) {
	root := setupTempRoot(t)

	// A large file ensures many writes. The throttled writer sleeps on each
	// write, forcing the transfer to outlast the 5ms reporting interval so
	// the periodic ticker fires more than once.
	big := filepath.Join(root, "big.bin")
	data := bytes.Repeat([]byte("x"), 4<<20) // 4 MiB
	if err := os.WriteFile(big, data, 0o644); err != nil {
		t.Fatalf("write big file: %v", err)
	}

	logBuf, logger := captureLogs()
	h := fileHandler(root, logger, 5*time.Millisecond)

	rec := &throttledResponseWriter{}
	h.ServeHTTP(rec, &http.Request{Method: http.MethodGet, URL: mustParseURL("/big.bin")})

	if !bytes.Equal(rec.buf.Bytes(), data) {
		t.Fatalf("served body size = %d, want %d", rec.buf.Len(), len(data))
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "download complete") {
		t.Fatalf("expected download complete log, got: %q", logs)
	}
	if !strings.Contains(logs, "interval speed") {
		t.Fatalf("expected interval speed log, got: %q", logs)
	}
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestFileHandlerShortSpeedIntervalIsFast(t *testing.T) {
	// Verify the transfer functions correctly even when no periodic tick
	// fires (short transfer vs 5s interval), and only the final log shows.
	root := setupTempRoot(t)
	_, logger := captureLogs()
	h := fileHandler(root, logger, 0)

	req := httptest.NewRequest(http.MethodGet, "/hello.txt", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, req)
	if time.Since(start) > 5*time.Second {
		t.Fatal("handler should not block for the full ticker interval")
	}
}

// --- loadConfig tests ---

func TestLoadConfigValid(t *testing.T) {
	root := t.TempDir()
	port, gotRoot, err := loadConfig([]string{"videosrv", "8080", root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != "8080" {
		t.Fatalf("port = %q, want 8080", port)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
}

func TestLoadConfigWrongArgCount(t *testing.T) {
	if _, _, err := loadConfig([]string{"videosrv", "8080"}); err == nil {
		t.Fatal("expected error for wrong arg count")
	}
	if _, _, err := loadConfig([]string{"videosrv", "8080", "a", "b"}); err == nil {
		t.Fatal("expected error for too many args")
	}
}

func TestLoadConfigInvalidPort(t *testing.T) {
	if _, _, err := loadConfig([]string{"videosrv", "notaport", "."}); err == nil {
		t.Fatal("expected error for non-numeric port")
	}
}

func TestLoadConfigMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, _, err := loadConfig([]string{"videosrv", "8080", missing}); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestLoadConfigNonDirectory(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, _, err := loadConfig([]string{"videosrv", "8080", f}); err == nil {
		t.Fatal("expected error for non-directory path")
	}
}

func TestAvg(t *testing.T) {
	if got := avg(1024*10, 10*time.Second); got != 1.0 {
		t.Fatalf("avg = %v, want 1.0", got)
	}
	if got := avg(100, 0); got != 0 {
		t.Fatalf("avg with zero duration = %v, want 0", got)
	}
	if got := avg(100, -time.Second); got != 0 {
		t.Fatalf("avg with negative duration = %v, want 0", got)
	}
}
