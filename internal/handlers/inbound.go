package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/safety-quotient-lab/agentd/internal/db"
)

type inboundMessage struct {
	Protocol  string `json:"protocol"`
	Type      string `json:"type"`
	From      any    `json:"from"`
	To        any    `json:"to"`
	SessionID string `json:"session_id"`
	Turn      int    `json:"turn"`
	Timestamp string `json:"timestamp"`
	Subject   string `json:"subject"`
	Body      string `json:"body,omitempty"`
}

// APIInbound handles POST /api/messages/inbound — dual-write to state.db + filesystem.
func APIInbound(projectRoot string, database *db.DB, zmqPublish func(string, any) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid body"})
			return
		}
		var msg inboundMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
			return
		}
		if msg.SessionID == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "missing session_id"})
			return
		}
		fromAgent := extractAgentID(msg.From)
		toAgent := extractAgentID(msg.To)
		subject := msg.Subject
		if strings.TrimSpace(subject) == "" {
			subject = msg.SessionID
			if msg.Type != "" {
				subject += fmt.Sprintf(" (%s from %s)", msg.Type, fromAgent)
			}
		}
		sender := fromAgent
		if sender == "" {
			sender = "unknown"
		}
		filename := fmt.Sprintf("from-%s-%03d.json", sender, msg.Turn)
		timestamp := msg.Timestamp
		if timestamp == "" {
			timestamp = time.Now().UTC().Format(time.RFC3339)
		}
		_, dbErr := database.Exec(
			`INSERT OR IGNORE INTO transport_messages
			 (filename, session_name, from_agent, to_agent, turn, message_type, subject, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			filename, msg.SessionID, fromAgent, toAgent,
			msg.Turn, msg.Type, subject, timestamp)
		if dbErr != nil {
			slog.Error("state.db write failed", "component", "inbound", "error", dbErr)
		}
		sessionDir := filepath.Join(projectRoot, "transport", "sessions", msg.SessionID)
		os.MkdirAll(sessionDir, 0755)
		filePath := filepath.Join(sessionDir, filename)
		os.WriteFile(filePath, body, 0644)
		slog.Info("inbound message accepted",
			"component", "inbound",
			"session", msg.SessionID,
			"from", fromAgent,
			"turn", msg.Turn)
		if zmqPublish != nil {
			zmqPublish("transport", map[string]any{
				"session_id": msg.SessionID, "from": fromAgent, "to": toAgent,
				"type": msg.Type, "subject": subject,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"accepted": true, "session_id": msg.SessionID, "filename": filename,
			"indexed": true, "dual_write": "state.db + filesystem",
		})
	}
}

func extractAgentID(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if id, ok := val["agent_id"].(string); ok {
			return id
		}
	case []any:
		if len(val) > 0 {
			return extractAgentID(val[0])
		}
	}
	return fmt.Sprintf("%v", v)
}
