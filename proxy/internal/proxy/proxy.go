package proxy

import (
	"bytes"
	"net/http"
)

// responseCapture wraps http.ResponseWriter to buffer the response body
// so callers can inspect the forwarded response.
type responseCapture struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	rc.buf.Write(b)
	return rc.ResponseWriter.Write(b)
}

func (rc *responseCapture) Flush() {
	if f, ok := rc.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
