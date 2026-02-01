package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type logCaptureSession struct {
	startedAt time.Time
	mode      string // journal|file
	file      string
	offset    int64
	target    string
}

var (
	logCaptureMu sync.Mutex
	logCaptures  = map[string]*logCaptureSession{}
)

func startLogCapture(reqID string, target string) map[string]any {
	if reqID == "" {
		return map[string]any{"success": false, "message": "missing requestId"}
	}
	if target == "" {
		target = "gost"
	}
	if target != "gost" && target != "agent" {
		return map[string]any{"success": false, "message": "unsupported target"}
	}
	sess := &logCaptureSession{startedAt: time.Now(), target: target}
	if target == "gost" {
		if file := findGostLogFile(); file != "" {
			sess.file = file
			if st, err := os.Stat(file); err == nil {
				sess.offset = st.Size()
			}
		}
	} else if target == "agent" {
		if file := findAgentLogFile(); file != "" {
			sess.file = file
			if st, err := os.Stat(file); err == nil {
				sess.offset = st.Size()
			}
		}
	}
	if hasJournalctl() {
		sess.mode = "journal"
	} else if sess.file != "" {
		sess.mode = "file"
	} else {
		sess.mode = "none"
	}
	logCaptureMu.Lock()
	logCaptures[reqID] = sess
	logCaptureMu.Unlock()
	return map[string]any{"success": true, "source": sess.mode}
}

func stopLogCapture(reqID string, target string) map[string]any {
	if reqID == "" {
		return map[string]any{"success": false, "message": "missing requestId"}
	}
	logCaptureMu.Lock()
	sess := logCaptures[reqID]
	delete(logCaptures, reqID)
	logCaptureMu.Unlock()
	if sess == nil {
		return map[string]any{"success": false, "message": "capture not found"}
	}
	if target == "" {
		target = "gost"
	}
	if target != "gost" && target != "agent" {
		return map[string]any{"success": false, "message": "unsupported target"}
	}
	if sess.target != "" {
		target = sess.target
	}
	switch sess.mode {
	case "journal":
		out, err := readJournalSince(sess.startedAt, target, "")
		if err == nil && !isJournalEmpty(out) {
			return map[string]any{"success": true, "source": "journal", "log": out}
		}
		if out2, ok2 := readJournalWithFallback(sess.startedAt, target); ok2 && !isJournalEmpty(out2) {
			return map[string]any{"success": true, "source": "journal", "log": out2}
		}
		// fallback to file if possible
		if sess.file != "" {
			if out2, err2 := readFileFromOffset(sess.file, sess.offset); err2 == nil {
				return map[string]any{"success": true, "source": "file", "log": out2}
			}
		}
		if err != nil {
			return map[string]any{"success": false, "message": err.Error()}
		}
		return map[string]any{"success": false, "message": "no journal entries"}
	case "file":
		if sess.file == "" {
			return map[string]any{"success": false, "message": "log file not found"}
		}
		out, err := readFileFromOffset(sess.file, sess.offset)
		if err != nil {
			return map[string]any{"success": false, "message": err.Error()}
		}
		return map[string]any{"success": true, "source": sess.file, "log": out}
	default:
		return map[string]any{"success": false, "message": "no log source"}
	}
}

func hasJournalctl() bool {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return false
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	return true
}

func readJournalSince(t time.Time, target string, unit string) (string, error) {
	sec := t.Unix()
	if unit == "" {
		if target == "agent" {
			unit = "flux-agent"
		} else {
			unit = "gost"
		}
	}
	cmd := exec.Command("journalctl", "-u", unit, "--since", fmt.Sprintf("@%d", sec), "--no-pager")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("journalctl error: %v", err)
	}
	return string(out), nil
}

func readJournalWithFallback(t time.Time, target string) (string, bool) {
	units := []string{}
	if target == "agent" {
		units = []string{"flux-agent", "flux-agent.service", "flux-agent2", "flux-agent2.service"}
	} else {
		units = []string{"gost", "gost.service"}
	}
	for _, u := range units {
		out, err := readJournalSince(t, target, u)
		if err == nil && !isJournalEmpty(out) {
			return out, true
		}
	}
	return "", false
}

func isJournalEmpty(out string) bool {
	if out == "" {
		return true
	}
	trimmed := bytes.TrimSpace([]byte(out))
	if len(trimmed) == 0 {
		return true
	}
	if bytes.Contains(trimmed, []byte("-- No entries --")) {
		return true
	}
	return false
}

func findGostLogFile() string {
	candidates := []string{
		"/var/log/gost.log",
		"/etc/gost/gost.log",
		"/var/log/gost/daemon.log",
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	// try detect /etc/gost/*.log
	if list, err := filepath.Glob("/etc/gost/*.log"); err == nil {
		for _, p := range list {
			if st, err2 := os.Stat(p); err2 == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

func findAgentLogFile() string {
	candidates := []string{
		"/var/log/flux-agent.log",
		"/var/log/flux-agent.err",
		"/etc/gost/flux-agent.log",
		"/var/log/flux-agent/daemon.log",
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func readFileFromOffset(path string, offset int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", err
		}
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, f); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return buf.String(), nil
}
