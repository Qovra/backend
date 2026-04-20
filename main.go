package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Qovra/hytale-backend/internal/api"
	"github.com/Qovra/hytale-backend/internal/database"
	"github.com/joho/godotenv"
)

func main() {
	// Root environment variables loader
	_ = godotenv.Load("../.env")

	port := os.Getenv("PANEL_PORT")
	if port == "" {
		port = "3000"
	}

	ctx := context.Background()

	// Initialize Postgres Connection
	if err := database.Init(ctx); err != nil {
		log.Fatalf("Fatal: Panel backend database connection failed: %v", err)
	}
	defer database.Close()

	mux := http.NewServeMux()

	// Public Routes
	mux.HandleFunc("/api/login", api.HandleLogin)

	// Protected Session Routes
	mux.HandleFunc("/api/logout", api.WithAuth(api.HandleLogout))
	mux.HandleFunc("/api/servers", api.WithAuth(api.HandleListServers))

	// Admin/Staff Protected Routing
	mux.HandleFunc("/api/nodes", api.RequireRole([]string{"admin", "staff"}, api.HandleListNodes))
	mux.HandleFunc("/api/nodes/create", api.RequireRole([]string{"admin", "staff"}, api.HandleCreateNode))
	mux.HandleFunc("/api/overview/stats", api.WithAuth(api.HandleGetMasterStats))
	mux.HandleFunc("/api/node/master/action", api.WithAuth(api.HandleMasterProxyAction))

	// Servers Creation triggers node HTTP payloads natively
	mux.HandleFunc("/api/servers/create", api.RequireRole([]string{"admin", "staff"}, api.HandleCreateServer))
	mux.HandleFunc("/api/internal/servers/", api.HandleUpdateProgress) // Captures /api/internal/servers/:id/progress
	mux.HandleFunc("/api/servers/delete", api.WithAuth(api.HandleDeleteServer))

	// Relay commands seamlessly to Node HTTP interfaces natively forwarding queries preserving JSON contexts
	mux.HandleFunc("/api/servers/status", api.WithAuth(api.ForwardNodeAction("status")))
	mux.HandleFunc("/api/servers/start", api.WithAuth(api.ForwardNodeAction("start")))
	mux.HandleFunc("/api/servers/stop", api.WithAuth(api.ForwardNodeAction("stop")))
	mux.HandleFunc("/api/servers/restart", api.WithAuth(api.ForwardNodeAction("restart")))
	mux.HandleFunc("/api/servers/logs", api.WithAuth(api.ForwardNodeAction("logs")))

	// ── Static Panel (React SPA) ──────────────────────────────────────────────
	// Reads PANEL_DIST_PATH from .env; falls back to /opt/qovra/panel/dist.
	// Every non-API, non-asset path returns index.html so React Router works.
	distPath := os.Getenv("PANEL_DIST_PATH")
	if distPath == "" {
		distPath = "/opt/qovra/panel/dist"
	}
	mux.HandleFunc("/", spaHandler(distPath))

	// Allow extremely forgiving CORS to connect our React cleanly
	handler := corsMiddleware(mux)

	log.Printf("[main] Hytale-Backend Master API initialized on port :%s", port)
	log.Printf("[main] Serving React panel from: %s", distPath)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatalf("Server crashed: %v", err)
	}
}

// spaHandler serves static files from distPath and falls back to index.html
// for any path that doesn't correspond to a real file (SPA client-side routing).
func spaHandler(distPath string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(distPath))
	return func(w http.ResponseWriter, r *http.Request) {
		absPath := filepath.Join(distPath, filepath.Clean("/"+r.URL.Path))
		info, err := os.Stat(absPath)
		// Serve index.html when the path doesn't exist OR is a directory
		if os.IsNotExist(err) || (err == nil && info.IsDir()) {
			http.ServeFile(w, r, filepath.Join(distPath, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
