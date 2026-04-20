package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/Qovra/hytale-backend/internal/database"
)

type ProgressRequest struct {
	Progress   int    `json:"progress"`
	Installing bool   `json:"installing"`
	Status     string `json:"status"`
	AuthURL    string `json:"auth_url,omitempty"`
	AuthCode   string `json:"auth_code,omitempty"`
}

// HandleUpdateProgress is an internal endpoint where Daemons report installation progress.
// Secured by DAEMON_API_TOKEN.
func HandleUpdateProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Auth Check
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token != os.Getenv("DAEMON_API_TOKEN") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. Parse ID from URL
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 5 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	serverID := pathParts[4] // /api/internal/servers/:id/progress

	var req ProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 3. Update DB
	query := `
		UPDATE servers 
		SET install_progress = $1, installing = $2, status = $3, 
		    auth_url = $4, auth_code = $5, updated_at = NOW() 
		WHERE id = $6
	`
	_, err := database.Pool.Exec(r.Context(), query, req.Progress, req.Installing, req.Status, req.AuthURL, req.AuthCode, serverID)
	if err != nil {
		http.Error(w, "DB update failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "progress updated"})
}
