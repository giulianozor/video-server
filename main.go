package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// speedInterval is how often download speed is printed during a transfer.
const speedInterval = 5 * time.Second

// trackingWriter wraps an http.ResponseWriter and counts bytes written.
type trackingWriter struct {
	http.ResponseWriter
	written *atomic.Int64
}

func (tw *trackingWriter) Write(p []byte) (int, error) {
	n, err := tw.ResponseWriter.Write(p)
	tw.written.Add(int64(n))
	return n, err
}

func fileHandler(root string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(root))

	return func(w http.ResponseWriter, r *http.Request) {
		// Only report speed for GET requests on actual files (not directory listings).
		if r.Method != http.MethodGet {
			fs.ServeHTTP(w, r)
			return
		}

		var totalBytes atomic.Int64
		tw := &trackingWriter{ResponseWriter: w, written: &totalBytes}

		start := time.Now()
		done := make(chan struct{})

		// Ticker goroutine: print speed every 5 seconds.
		go func() {
			ticker := time.NewTicker(speedInterval)
			defer ticker.Stop()
			var lastBytes int64
			var lastTime = start
			for {
				select {
				case <-done:
					return
				case t := <-ticker.C:
					cur := totalBytes.Load()
					elapsed := t.Sub(lastTime).Seconds()
					intervalSpeed := float64(cur-lastBytes) / elapsed / 1024
					avgSpeed := float64(cur) / time.Since(start).Seconds() / 1024
					log.Printf("[%s] interval speed: %.2f KB/s  avg speed: %.2f KB/s",
						r.URL.Path, intervalSpeed, avgSpeed)
					lastBytes = cur
					lastTime = t
				}
			}
		}()

		fs.ServeHTTP(tw, r)
		close(done)

		total := totalBytes.Load()
		elapsed := time.Since(start).Seconds()
		avgSpeed := 0.0
		if elapsed > 0 {
			avgSpeed = float64(total) / elapsed / 1024
		}
		log.Printf("[%s] download complete: %d bytes in %.2fs  avg speed: %.2f KB/s",
			r.URL.Path, total, elapsed, avgSpeed)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <port> <path>\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  port  TCP port to listen on\n")
	fmt.Fprintf(os.Stderr, "  path  directory to serve files from\n")
	os.Exit(1)
}

func main() {
	if len(os.Args) != 3 {
		usage()
	}

	port := os.Args[1]
	if _, err := strconv.Atoi(port); err != nil {
		fmt.Fprintf(os.Stderr, "invalid port %q: %v\n", port, err)
		usage()
	}

	root := os.Args[2]
	info, err := os.Stat(root)
	if err != nil {
		log.Fatalf("cannot access path %q: %v", root, err)
	}
	if !info.IsDir() {
		log.Fatalf("path %q is not a directory", root)
	}

	addr := ":" + port
	log.Printf("serving %q on http://0.0.0.0%s", root, addr)

	http.HandleFunc("/", fileHandler(root))
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
