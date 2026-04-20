package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Qovra/hytale-backend/internal/database"
	"github.com/golang-jwt/jwt/v5"
)

// SessionUser holds context info extracted from the DB
type SessionUser struct {
	ID   string
	Role string
}

type contextKey string
const userContextKey contextKey = "sessionUser"

// WithAuth verifies the Bearer token against the user_sessions table in Postgres.
func WithAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Handle CORS preflight
		if r.Method == http.MethodOptions {
			next(w, r)
			return
		}

		token := r.Header.Get("Authorization")
		if token == "" {
			// Fallback to query param for SSE / WebSocket connections that can't set headers
			if qp := r.URL.Query().Get("token"); qp != "" {
				token = "Bearer " + qp
			}
		}
		if token == "" {
			http.Error(w, "Missing Authorization Header", http.StatusUnauthorized)
			return
		}

		if strings.HasPrefix(token, "Bearer ") {
			token = token[7:]
		}

		var su SessionUser

		// Parse JWT token
		parsedToken, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		})

		if err != nil || !parsedToken.Valid {
			log.Printf("[auth] JWT parse failed for token %s...: %v", token[:10], err)
			http.Error(w, "Invalid or expired JWT token", http.StatusUnauthorized)
			return
		}

		claims, ok := parsedToken.Claims.(jwt.MapClaims)
		if !ok {
			log.Printf("[auth] Invalid JWT claims structure")
			http.Error(w, "Invalid token claims", http.StatusUnauthorized)
			return
		}

		su.ID = claims["id"].(string)
		su.Role = claims["role"].(string)

		// Verification in DB for revocation ("guardado en user_sessions")
		var expiresAt time.Time
		dbQuery := "SELECT expires_at FROM user_sessions WHERE token = $1"
		err = database.Pool.QueryRow(r.Context(), dbQuery, token).Scan(&expiresAt)
		if err != nil {
			log.Printf("[auth] Session not found in DB for user %s: %v", su.ID, err)
			http.Error(w, "Session revoked or not found", http.StatusUnauthorized)
			return
		}

		if time.Now().After(expiresAt) {
			log.Printf("[auth] Session expired in DB for user %s", su.ID)
			http.Error(w, "Session expired (DB)", http.StatusUnauthorized)
			return
		}

		// Attach user context to request
		ctx := context.WithValue(r.Context(), userContextKey, su)
		next(w, r.WithContext(ctx))
	}
}

// RequireRole wraps endpoints strictly blocking lower tiers.
func RequireRole(allowedRoles []string, next http.HandlerFunc) http.HandlerFunc {
	return WithAuth(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(userContextKey).(SessionUser)
		if !ok {
			http.Error(w, "User context missing", http.StatusInternalServerError)
			return
		}

		hasRole := false
		for _, role := range allowedRoles {
			if user.Role == role {
				hasRole = true
				break
			}
		}

		if !hasRole {
			http.Error(w, "Forbidden. Insufficient permissions.", http.StatusForbidden)
			return
		}

		next(w, r)
	})
}
