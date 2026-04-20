package api

import (
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
)

var allowedUnits = map[string]string{
	"daemon":  "qovra-daemon",
	"proxy":   "qovra-proxy",
	"backend": "qovra-backend",
}

// HandleStreamLogs streams journalctl output for a given service via Server-Sent Events.
func HandleStreamLogs(w http.ResponseWriter, r *http.Request) {
	// Extract service from path: /api/logs/{service}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/logs/"), "/")
	serviceID := parts[0]

	unit, ok := allowedUnits[serviceID]
	if !ok {
		http.Error(w, "unknown service: "+serviceID, http.StatusBadRequest)
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	cmd := exec.CommandContext(ctx, "journalctl", "-u", unit, "-f", "-n", "100", "--no-pager")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "failed to open pipe", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[logs] Failed to start journalctl for %s: %v", unit, err)
		fmt.Fprintf(w, "data: [ERR] journalctl not available: %s\n\n", err.Error())
		flusher.Flush()
		return
	}

	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return
		default:
			n, err := stdout.Read(buf)
			if n > 0 {
				lines := strings.Split(string(buf[:n]), "\n")
				for _, line := range lines {
					if line == "" {
						continue
					}
					fmt.Fprintf(w, "data: %s\n\n", line)
				}
				flusher.Flush()
			}
			if err != nil {
				return
			}
		}
	}
}
