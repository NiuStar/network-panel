package controller

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/panelupgrade"
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
	b, _ := json.Marshal(map[string]any{"event": event, "time": time.Now().Format(time.RFC3339Nano), "data": data})
	fmt.Fprintf(s.buf, "data: %s\n\n", b)
	s.buf.Flush()
	s.fl.Flush()
}

// VersionUpgradeStream is read-only. Reconnecting can never trigger another upgrade.
func VersionUpgradeStream(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, response.ErrMsg("请通过 POST /version/upgrade 创建事务并提供 id"))
		return
	}
	status, err := panelupgrade.LoadStatus(id)
	if err != nil {
		c.JSON(http.StatusNotFound, response.ErrMsg("升级事务状态不可用"))
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	sw := newStreamWriter(c)
	timeout := time.NewTimer(3 * time.Minute)
	defer timeout.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	for {
		if status.State != last {
			sw.send("status", status)
			last = status.State
		}
		if status.State == "COMMITTED" {
			sw.send("done", status)
			return
		}
		if panelupgrade.Terminal(status.State) || status.State == "ROLLBACK_FAILED" || status.State == "RECOVERY_REQUIRED" {
			sw.send("error", status)
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-timeout.C:
			sw.send("error", "状态等待超时，升级结果尚未确认，请根据事务 ID 查询")
			return
		case <-ticker.C:
			status, err = panelupgrade.LoadStatus(id)
			if err != nil {
				sw.send("error", "升级事务状态读取失败，结果尚未确认")
				return
			}
		}
	}
}
