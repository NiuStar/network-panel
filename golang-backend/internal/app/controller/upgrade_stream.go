package controller

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type streamWriter struct {
	w   http.ResponseWriter
	fl  http.Flusher
	buf *bufio.Writer
}

func newStreamWriter(c *gin.Context) *streamWriter {
	w := c.Writer
	fl, _ := w.(http.Flusher)
	return &streamWriter{w: w, fl: fl, buf: bufio.NewWriter(w)}
}

func (s *streamWriter) send(event string, data any) {
	if s.fl == nil {
		return
	}
	payload := map[string]any{
		"event": event,
		"time":  time.Now().Format(time.RFC3339Nano),
		"data":  data,
	}
	b, _ := json.Marshal(payload)
	fmt.Fprintf(s.buf, "data: %s\n\n", b)
	s.buf.Flush()
	s.fl.Flush()
}

// VersionUpgradeStream streams upgrade logs via SSE-like chunked response.
func VersionUpgradeStream(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	proxyPrefix := c.Query("proxyPrefix")
	sw := newStreamWriter(c)
	logf := func(format string, a ...any) {
		sw.send("log", fmt.Sprintf(format, a...))
	}

	_, _, errs, post := runUpgradeWithRestart(proxyPrefix, logf)
	if len(errs) > 0 {
		sw.send("error", strings.Join(errs, "; "))
	}
	sw.send("done", "升级完成")
	if post != nil {
		go post()
	}
}
