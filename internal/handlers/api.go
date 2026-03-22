package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/safety-quotient-lab/agentd/internal/collector"
)

// allowedOrigins defines CORS allowlisted origins.
var allowedOrigins = map[string]bool{
	"https://mesh.safety-quotient.dev":             true,
	"https://interagent.safety-quotient.dev":       true,
	"https://psychology-agent.safety-quotient.dev": true,
	"https://psq-agent.safety-quotient.dev":        true,
	"https://psy-session.safety-quotient.dev":      true,
	"https://api.safety-quotient.dev":              true,
	"https://unratified-agent.unratified.org":      true,
	"https://observatory-agent.unratified.org":     true,
	"http://localhost:8076":                         true,
	"http://localhost:8077":                         true,
	"http://localhost:8078":                         true,
	"http://localhost:8079":                         true,
	"http://localhost:9000":                         true,
}

func corsOrigin(origin string) string {
	if allowedOrigins[origin] {
		return origin
	}
	return ""
}

func setCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if cors := corsOrigin(origin); cors != "" {
		w.Header().Set("Access-Control-Allow-Origin", cors)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Vary", "Origin")
	}
}

// SetCORS applies CORS headers (exported for use in main.go inline handlers).
func SetCORS(w http.ResponseWriter, r *http.Request) {
	setCORS(w, r)
}

// HandlePreflight returns 204 for OPTIONS requests with CORS headers.
func HandlePreflight(w http.ResponseWriter, r *http.Request) {
	setCORS(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// setAPIHeaders sets CORS + Cache-Control for JSON API responses.
// max-age matches the meshd cache TTL so CF edge caching stays coherent.
func setAPIHeaders(w http.ResponseWriter, r *http.Request) {
	setCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=10")
}

// APIStatus serves GET /api/status — backward-compatible JSON.
func APIStatus(cache *collector.Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAPIHeaders(w, r)
		status := cache.Status()
		json.NewEncoder(w).Encode(status)
	}
}

// AgentCard serves /.well-known/agent-card.json with the agent ID
// injected from .agent-identity.json. This allows the same project
// directory to serve distinct identities for different agentd instances
// (e.g., psychology-agent on Chromabook vs psy-session on gray-box).
func AgentCard(projectRoot, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w, r)
		cardPath := filepath.Join(projectRoot, ".well-known", "agent-card.json")
		data, err := os.ReadFile(cardPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// Patch the "name" field to match the running agent's identity
		var card map[string]any
		if json.Unmarshal(data, &card) == nil {
			if name, ok := card["name"].(string); ok && name != agentID {
				card["name"] = agentID
				data, _ = json.MarshalIndent(card, "", "  ")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(data)
	}
}

// HealthCheck serves HEAD / and GET /health.
func HealthCheck() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}
}
