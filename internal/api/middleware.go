package api

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack keeps WebSocket upgrades working through the logging middleware.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return h.Hijack()
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func isDashboardPoll(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/devices", "/api/v1/calls/active", "/api/v1/cdr",
		"/api/v1/alarms", "/api/v1/broadcast/active", "/api/v1/conference",
		"/api/v1/paging", "/api/v1/paging/events":
		return true
	}
	return false
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		// The dashboard polls six read-only endpoints every two seconds.
		// Successful polls are noise; errors and state-changing requests are
		// still logged.
		if sw.code == 200 && isDashboardPoll(r) {
			return
		}
		log.Printf("http %s %s %d %s", r.Method, r.URL.Path, sw.code, time.Since(start).Round(time.Millisecond))
	})
}
