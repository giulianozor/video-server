package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// speedInterval is how often download speed is printed during a transfer.
const speedInterval = 5 * time.Second

// trackingWriter wraps an http.ResponseWriter and counts bytes written.
// It forwards writes to the underlying writer while tracking the total
// number of bytes sent and the response status, allowing the caller to
// compute download speeds.
type trackingWriter struct {
	http.ResponseWriter
	written  *atomic.Int64
	status   int
	wroteHdr bool
}

func (tw *trackingWriter) Write(p []byte) (int, error) {
	n, err := tw.ResponseWriter.Write(p)
	tw.written.Add(int64(n))
	return n, err
}

// WriteHeader records the response status before forwarding it to the
// underlying writer.
func (tw *trackingWriter) WriteHeader(code int) {
	tw.status = code
	tw.wroteHdr = true
	tw.ResponseWriter.WriteHeader(code)
}

// success reports whether the response status indicates a successful
// transfer (2xx), defaulting to true when no explicit header was written.
func (tw *trackingWriter) success() bool {
	if !tw.wroteHdr {
		return true
	}
	return tw.status >= http.StatusOK && tw.status < http.StatusMultipleChoices
}

// Flush flushes any buffered data to the client, preserving the optional
// http.Flusher interface exposed by the underlying ResponseWriter.
func (tw *trackingWriter) Flush() {
	if f, ok := tw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom efficiently copies data from r into the underlying writer while
// still tracking the number of bytes written. It preserves the optional
// io.ReaderFrom optimization used by http.ServeContent.
func (tw *trackingWriter) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := tw.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(r)
		tw.written.Add(n)
		return n, err
	}
	n, err := io.Copy(tw.ResponseWriter, r)
	tw.written.Add(n)
	return n, err
}

// Hijack lets a connection be taken over for raw TCP access, preserving the
// optional http.Hijacker interface.
func (tw *trackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := tw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
}

// Push initiates an HTTP/2 server push, preserving the optional http.Pusher
// interface.
func (tw *trackingWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := tw.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// fileHandler returns an http.HandlerFunc that serves files from root and logs
// download progress with speed metrics for GET requests. Report output is
// written to logger (nil means the process-level *log.Logger is used).
// interval controls how often the periodic speed report is emitted (a value
// <= 0 defaults to speedInterval).
func fileHandler(root string, logger *log.Logger, interval time.Duration) http.HandlerFunc {
	fs := http.FileServer(http.Dir(root))
	if logger == nil {
		logger = log.Default()
	}
	if interval <= 0 {
		interval = speedInterval
	}

	return func(w http.ResponseWriter, r *http.Request) {
		// Only report speed for GET requests; everything else is a plain
		// pass-through to the file server.
		if r.Method != http.MethodGet {
			fs.ServeHTTP(w, r)
			return
		}

		var totalBytes atomic.Int64
		tw := &trackingWriter{ResponseWriter: w, written: &totalBytes}

		start := time.Now()
		done := make(chan struct{})
		defer close(done)

		// Ticker goroutine: print interval and average speed every
		// `interval` until the transfer completes.
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			var lastBytes int64
			lastTime := start
			for {
				select {
				case <-done:
					return
				case t := <-ticker.C:
					cur := totalBytes.Load()
					elapsed := max(t.Sub(lastTime).Seconds(), 0)
					intervalSpeed := float64(cur-lastBytes) / 1024
					if elapsed > 0 {
						intervalSpeed /= elapsed
					}
					avgSpeed := avg(int64(cur), time.Since(start))
					logger.Printf("[%s] interval speed: %.2f KB/s  avg speed: %.2f KB/s",
						r.URL.Path, intervalSpeed, avgSpeed)
					lastBytes = cur
					lastTime = t
				}
			}
		}()

		fs.ServeHTTP(tw, r)

		if !tw.success() {
			return
		}
		total := totalBytes.Load()
		elapsed := time.Since(start)
		avgSpeed := avg(total, elapsed)
		logger.Printf("[%s] download complete: %d bytes in %.2fs  avg speed: %.2f KB/s",
			r.URL.Path, total, elapsed.Seconds(), avgSpeed)
	}
}

// avg returns the average throughput in KB/s over the given duration and
// byte count, returning 0 when the duration is zero.
func avg(total int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(total) / elapsed.Seconds() / 1024
}

func loadConfig(args []string) (string, string, error) {
	if len(args) != 3 {
		return "", "", fmt.Errorf("usage: %s <port> <path>", args[0])
	}

	port := args[1]
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", fmt.Errorf("invalid port %q: %v", port, err)
	}

	root := args[2]
	info, err := os.Stat(root)
	if err != nil {
		return "", "", fmt.Errorf("cannot access path %q: %v", root, err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("path %q is not a directory", root)
	}

	return port, root, nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <port> <path>\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  port  TCP port to listen on\n")
	fmt.Fprintf(os.Stderr, "  path  directory to serve files from\n")
}

func main() {
	port, root, err := loadConfig(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		os.Exit(1)
	}

	addr := ":" + port
	log.Printf("serving %q on http://0.0.0.0%s", root, addr)

	http.HandleFunc("/", fileHandler(root, nil, speedInterval))
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
