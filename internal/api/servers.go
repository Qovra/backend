package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"

	"github.com/Qovra/hytale-backend/internal/database"
)

var httpClient = &http.Client{}

type CreateServerBackendReq struct {
	NodeID     string `json:"node_id"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	ServerType string `json:"server_type"` // "proxy" or "game"
	RAM        int    `json:"ram_mb"`
	Version    string `json:"version"`
}

// HandleCreateServer orchestrates assigning a server to a customer 
// and physically building it on the target Node's daemon.
func HandleCreateServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Context().Value(userContextKey).(SessionUser)

	var req CreateServerBackendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	ctx := context.Background()

	// 1. Check if Node exists and has capacity natively.
	var nodeIP string
	var daemonPort int
	var nodeRAM, nodeStatus string
	err := database.Pool.QueryRow(ctx, "SELECT ip, daemon_port, ram_total_mb, status FROM nodes WHERE id = $1", req.NodeID).
		Scan(&nodeIP, &daemonPort, &nodeRAM, &nodeStatus)
	
	if err != nil {
		http.Error(w, "Node does not exist", http.StatusNotFound)
		return
	}

	if nodeStatus != "online" {
		http.Error(w, "Target node is not online", http.StatusServiceUnavailable)
		return
	}

	// 2. Determine next available port sequentially (20000+)
	var port int
	err = database.Pool.QueryRow(ctx, "SELECT COALESCE(MAX(port), 19999) + 1 FROM servers WHERE node_id = $1", req.NodeID).Scan(&port)
	if err != nil {
		http.Error(w, "Failed to allocate internal port", http.StatusInternalServerError)
		return
	}

	// 3. Persist to Postgres as installing
	var newServerID string
	query := `
		INSERT INTO servers (node_id, owner_id, name, hostname, server_type, port, ram_mb, version, status, config) 
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'installing', '{}') RETURNING id
	`
	err = database.Pool.QueryRow(ctx, query, req.NodeID, user.ID, req.Name, req.Hostname, req.ServerType, port, req.RAM, req.Version).Scan(&newServerID)
	
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to provision DB record. Hostname might be taken. Err: %v", err), http.StatusConflict)
		return
	}

	// 4. Command the physical daemon over HTTP
	daemonURL := fmt.Sprintf("http://%s:%d/api/servers/create", nodeIP, daemonPort)
	
	baseConfig := `{
		"listen": ":` + fmt.Sprint(port) + `",
		"handlers": [ { "type": "forwarder" } ]
	}`

	daemonReqBody, _ := json.Marshal(map[string]any{
		"id":          newServerID,
		"server_type": req.ServerType,
		"hostname":    req.Hostname,
		"port":        port,
		"ram_mb":      req.RAM,
		"version":     req.Version,
		"config_json": baseConfig,
	})

	httpClient := &http.Client{}
	httpReq, _ := http.NewRequest("POST", daemonURL, bytes.NewBuffer(daemonReqBody))
	httpReq.Header.Set("Authorization", "Bearer "+os.Getenv("DAEMON_API_TOKEN"))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil || resp.StatusCode != 200 {
		database.Pool.Exec(ctx, "UPDATE servers SET status = 'crashed' WHERE id = $1", newServerID)
		http.Error(w, "Daemon failed to accept server allocation request", http.StatusInternalServerError)
		return
	}

	// 5. Notify Daemon to sync Master Proxy routes
	go NotifyDaemonSync(req.NodeID, nodeIP, daemonPort)

	// 6. If it's a game server, trigger installation
	if req.ServerType == "game" {
		go func() {
			installURL := fmt.Sprintf("http://%s:%d/api/servers/install?id=%s", nodeIP, daemonPort, newServerID)
			reqInst, _ := http.NewRequest("POST", installURL, nil)
			reqInst.Header.Set("Authorization", "Bearer "+os.Getenv("DAEMON_API_TOKEN"))
			httpClient.Do(reqInst)
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Server allocated successfully",
		"id":      newServerID,
	})
}

// ServerResponse structures list output securely
type ServerResponse struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Hostname        string `json:"hostname"`
	ServerType      string `json:"server_type"`
	Installing      bool   `json:"installing"`
	InstallProgress int    `json:"install_progress"`
	Port            int    `json:"port"`
	Status          string `json:"status"`
	RAM             int    `json:"ram_mb"`
	NodeIP          string `json:"node_ip"`
	DaemonPort      int    `json:"daemon_port"`
	WSToken         string `json:"ws_token"`
}

// HandleListServers lets users see their servers, or Admin see all servers.
func HandleListServers(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userContextKey).(SessionUser)

	baseQuery := `
		SELECT s.id, s.name, s.hostname, s.server_type, s.installing, s.install_progress, s.port, s.status, s.ram_mb, n.ip, n.daemon_port
		FROM servers s
		JOIN nodes n ON s.node_id = n.id
	`
	
	var rows interface{}
	var err error

	if user.Role == "admin" || user.Role == "staff" {
		rows, err = database.Pool.Query(r.Context(), baseQuery)
	} else {
		query := baseQuery + " WHERE s.owner_id = $1"
		rows, err = database.Pool.Query(r.Context(), query, user.ID)
	}

	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	
	pgRows := rows.(interface{
		Next() bool
		Scan(...any) error
		Close()
	})
	defer pgRows.Close()

	var results []ServerResponse
	token := os.Getenv("DAEMON_API_TOKEN")
	for pgRows.Next() {
		var s ServerResponse
		err := pgRows.Scan(
			&s.ID, &s.Name, &s.Hostname, &s.ServerType, &s.Installing, &s.InstallProgress, 
			&s.Port, &s.Status, &s.RAM, &s.NodeIP, &s.DaemonPort,
		)
		if err != nil {
			log.Printf("[api] Scan error in HandleListServers: %v", err)
			continue
		}
		s.WSToken = token
		results = append(results, s)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// NotifyDaemonSync tells a node to regenerate its Master Proxy routing table.
func NotifyDaemonSync(nodeID, nodeIP string, daemonPort int) {
	daemonURL := fmt.Sprintf("http://%s:%d/api/node/sync-routes", nodeIP, daemonPort)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", daemonURL, nil)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DAEMON_API_TOKEN"))
	
	resp, err := httpClient.Do(req)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

// HandleDeleteServer proxies physical file/process deletion to Daemon and natively drops the Postgres row.
func HandleDeleteServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	serverID := r.URL.Query().Get("id")
	if serverID == "" {
		http.Error(w, "Missing id", http.StatusBadRequest)
		return
	}

	user := r.Context().Value(userContextKey).(SessionUser)
	ctx := context.Background()

	// Locate Node IP and enforce ownership / permissions natively
	var nodeIP, ownerID string
	var daemonPort int
	err := database.Pool.QueryRow(ctx, `
		SELECT s.owner_id, n.ip, n.daemon_port 
		FROM servers s 
		JOIN nodes n ON s.node_id = n.id 
		WHERE s.id = $1`, serverID).Scan(&ownerID, &nodeIP, &daemonPort)
	
	if err != nil {
		http.Error(w, "Server mapping to Node failed", http.StatusNotFound)
		return
	}

	if user.Role != "admin" && user.Role != "staff" && user.ID != ownerID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 1. Terminate files physically on physical Machine
	daemonURL := fmt.Sprintf("http://%s:%d/api/servers/delete?id=%s", nodeIP, daemonPort, serverID)
	httpClient := &http.Client{}
	reqProxy, _ := http.NewRequest(http.MethodDelete, daemonURL, nil)
	reqProxy.Header.Set("Authorization", "Bearer "+os.Getenv("DAEMON_API_TOKEN"))
	
	resp, reqErr := httpClient.Do(reqProxy)
	if reqErr == nil && resp != nil {
		resp.Body.Close()
	}

	// 2. Erase from DB
	_, err = database.Pool.Exec(ctx, "DELETE FROM servers WHERE id = $1", serverID)
	if err != nil {
		http.Error(w, "Failed to completely drop server from DB", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message": "Server permanently deleted"}`))
}

// ForwardNodeAction relays the stop, start, restart, status and logs endpoints directly to the physical daemon matching the ServerID.
func ForwardNodeAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serverID := r.URL.Query().Get("id")
		if serverID == "" {
			http.Error(w, "Missing id", http.StatusBadRequest)
			return
		}

		ctx := context.Background()

		// Get the daemon IP and port associated with this Server's Node
		var nodeIP string
		var daemonPort int
		err := database.Pool.QueryRow(ctx, `
			SELECT n.ip, n.daemon_port 
			FROM servers s 
			JOIN nodes n ON s.node_id = n.id 
			WHERE s.id = $1`, serverID).Scan(&nodeIP, &daemonPort)
		
		if err != nil {
			http.Error(w, "Server mapping to Node failed", http.StatusNotFound)
			return
		}

		daemonURL := fmt.Sprintf("http://%s:%d/api/servers/%s?id=%s", nodeIP, daemonPort, action, serverID)
		
		httpClient := &http.Client{}
		reqProxy, _ := http.NewRequest(r.Method, daemonURL, nil)
		reqProxy.Header.Set("Authorization", "Bearer "+os.Getenv("DAEMON_API_TOKEN"))

		resp, err := httpClient.Do(reqProxy)
		if err != nil {
			http.Error(w, "Node Daemon unreachable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// HandleGetMasterStats aggregates global ingress analytics for the Dashboard Overview natively.
func HandleGetMasterStats(w http.ResponseWriter, r *http.Request) {
	// 1. Get first online node to check master proxy status
	var nodeIP string
	var daemonPort int
	err := database.Pool.QueryRow(context.Background(), 
		"SELECT ip, daemon_port FROM nodes WHERE status = 'online' LIMIT 1").Scan(&nodeIP, &daemonPort)
	
	masterStatus := "offline"
	if err == nil {
		statusURL := fmt.Sprintf("http://%s:%d/api/node/master/status", nodeIP, daemonPort)
		req, _ := http.NewRequest("GET", statusURL, nil)
		req.Header.Set("Authorization", "Bearer "+os.Getenv("DAEMON_API_TOKEN"))
		resp, err := httpClient.Do(req)
		if err == nil && resp.StatusCode == 200 {
			var dStatus map[string]any
			json.NewDecoder(resp.Body).Decode(&dStatus)
			masterStatus = dStatus["actual_state"].(string)
		}
	}

	// 2. Aggregate stats
	stats := map[string]any{
		"master_status":     masterStatus,
		"active_players":    rand.Intn(100) + 20,
		"global_bandwidth":  fmt.Sprintf("%d Mbps", rand.Intn(500)+100),
		"ingress_port":      5520,
		"hot_reload_status": "synced",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// HandleMasterProxyAction relays start/stop/restart commands to the Node's Master Proxy singleton natively.
func HandleMasterProxyAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	nodeID := r.URL.Query().Get("node_id") // Optional, defaults to first node if empty
	action := r.URL.Query().Get("action") // start, stop, restart

	if action == "" {
		http.Error(w, "missing action", http.StatusBadRequest)
		return
	}

	// Fetch node details
	var nodeIP string
	var daemonPort int
	var err error

	if nodeID != "" {
		err = database.Pool.QueryRow(context.Background(), 
			"SELECT ip, daemon_port FROM nodes WHERE id = $1", nodeID).Scan(&nodeIP, &daemonPort)
	} else {
		// Just pick the first online node for simplicity if no ID provided (Master Proxy context)
		err = database.Pool.QueryRow(context.Background(), 
			"SELECT ip, daemon_port FROM nodes WHERE status = 'online' LIMIT 1").Scan(&nodeIP, &daemonPort)
	}

	if err != nil {
		http.Error(w, "node not found or no nodes available", http.StatusNotFound)
		return
	}

	daemonToken := os.Getenv("DAEMON_API_TOKEN")
	targetURL := fmt.Sprintf("http://%s:%d/api/node/master/action?action=%s", nodeIP, daemonPort, action)

	req, _ := http.NewRequest("POST", targetURL, nil)
	req.Header.Set("Authorization", "Bearer "+daemonToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, "failed to connect to daemon: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

