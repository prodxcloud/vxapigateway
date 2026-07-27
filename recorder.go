package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

// responseRecorder wraps an http.ResponseWriter to observe the status the
// upstream actually returned and whether anything has been committed to the
// client yet.
//
// Why it exists: httputil.ReverseProxy reports a failed round trip by calling
// its ErrorHandler, not by returning an error. Without observing the response,
// the gateway could not distinguish a proxied 502 from a healthy 200 — so every
// request was logged and counted as 200, and the retry/failover path never ran.
//
// Committed() is what makes retrying safe. Once a status line or a byte of body
// has gone out, the response cannot be restarted, so a mid-stream upstream
// failure must be surfaced rather than retried.
//
// The pass-throughs below are not optional decoration:
//
//   - Hijack: ReverseProxy hijacks the connection to proxy a WebSocket or any
//     other protocol upgrade. A wrapper that does not implement http.Hijacker
//     silently breaks every upgrade the gateway forwards.
//   - Flush: without it, streaming responses (SSE, chunked JSON, log tails) are
//     buffered until the handler returns, which turns a live stream into a long
//     silence.
//   - Unwrap: lets http.ResponseController reach the real writer for deadline
//     control on Go 1.20+.
type responseRecorder struct {
	http.ResponseWriter
	status    int
	committed bool
	bytes     int64
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w}
}

func (rr *responseRecorder) WriteHeader(code int) {
	if rr.committed {
		return // a second WriteHeader is a no-op, as in net/http
	}
	rr.status = code
	rr.committed = true
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if !rr.committed {
		// net/http infers 200 from a bare Write; record that rather than 0.
		rr.WriteHeader(http.StatusOK)
	}
	n, err := rr.ResponseWriter.Write(b)
	rr.bytes += int64(n)
	return n, err
}

// Status returns the observed status code, defaulting to 200 when the upstream
// wrote a body without an explicit status.
func (rr *responseRecorder) Status() int {
	if rr.status == 0 {
		return http.StatusOK
	}
	return rr.status
}

// Committed reports whether any status line or body byte has reached the client.
func (rr *responseRecorder) Committed() bool { return rr.committed }

// BytesWritten returns the number of body bytes forwarded to the client.
func (rr *responseRecorder) BytesWritten() int64 { return rr.bytes }

// Flush forwards a flush so streaming responses are not buffered.
func (rr *responseRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards a hijack so protocol upgrades (WebSocket, and anything else
// ReverseProxy switches protocols for) keep working through the wrapper.
func (rr *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rr.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("responseRecorder: %T is not an http.Hijacker", rr.ResponseWriter)
	}
	// A hijacked connection is written directly by the caller, so from the
	// gateway's point of view the response is committed and unretryable.
	rr.committed = true
	if rr.status == 0 {
		rr.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (rr *responseRecorder) Unwrap() http.ResponseWriter { return rr.ResponseWriter }
