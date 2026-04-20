package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Qovra/hytale-backend/internal/database"
)

type NodeResponse struct {
	ID          string `json:"id"`
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	DaemonPort  int    `json:"daemon_port"`
	RAMTotal    int    `json:"ram_total_mb"`
	RAMUsed     int    `json:"ram_used_mb"`
	Status      string `json:"status"`
}

type CreateNodeReq struct {
	Hostname   string `json:"hostname"`
	IP         string `json:"ip"`
	DaemonPort int    `json:"daemon_port"`
	RAMTotal   int    `json:"ram_total_mb"`
}

// HandleListNodes queries the nodes table, and calculates active RAM allocation
func HandleListNodes(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT n.id, n.hostname, n.ip, n.daemon_port, n.ram_total_mb, n.status,
		       COALESCE((SELECT SUM(ram_mb) FROM servers WHERE node_id = n.id AND status != 'stopped'), 0) as ram_used
		FROM nodes n
	`

	rows, err := database.Pool.Query(context.Background(), query)
	if err != nil {
		http.Error(w, "Database query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var nodes []NodeResponse
	for rows.Next() {
		var n NodeResponse
		if err := rows.Scan(&n.ID, &n.Hostname, &n.IP, &n.DaemonPort, &n.RAMTotal, &n.Status, &n.RAMUsed); err == nil {
			nodes = append(nodes, n)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nodes)
}

func HandleCreateNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CreateNodeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	var newID string
	query := `
		INSERT INTO nodes (hostname, ip, daemon_port, ram_total_mb, status)
		VALUES ($1, $2, $3, $4, 'offline')
		RETURNING id
	`
	err := database.Pool.QueryRow(context.Background(), query, 
		req.Hostname, req.IP, req.DaemonPort, req.RAMTotal).Scan(&newID)

	if err != nil {
		http.Error(w, "Failed to register Node, possibly duplicate Hostname", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Node successfully created",
		"id":      newID,
	})
}
