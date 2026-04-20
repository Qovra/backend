package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Qovra/hytale-backend/internal/database"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var jwtSecret = []byte(os.Getenv("JWT_SECRET"))

func init() {
	if len(jwtSecret) == 0 {
		jwtSecret = []byte("hytale-ops-development-secret-123456")
	}
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
	Role  string `json:"role"`
}



func HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var id string
	var hash string
	var role string

	err := database.Pool.QueryRow(context.Background(),
		"SELECT id, password, role FROM users WHERE email = $1", req.Email).
		Scan(&id, &hash, &role)

	if err != nil {
		// Do not differentiate between user not found and bad password
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	// Create JWT token fundamentally
	claims := jwt.MapClaims{
		"id":    id,
		"email": req.Email,
		"role":  role,
		"exp":   time.Now().Add(24 * time.Hour).Unix(),
	}
	
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		http.Error(w, "Failed to sign token", http.StatusInternalServerError)
		return
	}

	// Store in session (as requested: "guardado en user_sessions")
	expiresAtm := time.Now().Add(24 * time.Hour)
	_, err = database.Pool.Exec(context.Background(),
		"INSERT INTO user_sessions (user_id, token, expires_at) VALUES ($1, $2, $3)",
		id, tokenString, expiresAtm)
	
	if err != nil {
		http.Error(w, "Failed to persist session", http.StatusInternalServerError)
		return
	}

	log.Printf("[auth] Login successful for user %s. Issuing JWT: %s...", id, tokenString[:10])
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LoginResponse{Token: tokenString, Role: role})
}

func HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.Header.Get("Authorization") // Expected without Bearer for simplicity here or handled in middleware
	if len(token) > 7 && token[:7] == "Bearer " {
		token = token[7:]
	}

	if token != "" {
		database.Pool.Exec(context.Background(), "DELETE FROM user_sessions WHERE token = $1", token)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message": "Logged out"}`))
}
