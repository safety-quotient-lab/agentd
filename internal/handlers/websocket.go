package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/safety-quotient-lab/agentd/internal/collector"
	"nhooyr.io/websocket"
)

// WebSocketEvents serves GET /ws — WebSocket stream for real-time updates.
// Replaces SSE (/events) for Cloudflare Tunnel compatibility.
// Sends a JSON message whenever the cache generation changes.
func WebSocketEvents(cache *collector.Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Allow cross-origin connections (dashboard may load from different origin)
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer conn.CloseNow()

		ctx := r.Context()

		// Subscribe to cache change notifications
		notify := cache.Subscribe()
		defer cache.Unsubscribe(notify)

		// Send initial connected message
		gen := cache.Generation()
		msg, _ := json.Marshal(map[string]any{
			"event":      "connected",
			"generation": gen,
		})
		conn.Write(ctx, websocket.MessageText, msg)

		// Keepalive ticker
		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()

		for {
			select {
			case <-ctx.Done():
				conn.Close(websocket.StatusNormalClosure, "client disconnected")
				return

			case <-notify:
				newGen := cache.Generation()
				if newGen != gen {
					gen = newGen
					msg, _ := json.Marshal(map[string]any{
						"event":      "refresh",
						"generation": gen,
					})
					if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
						return
					}
				}

			case <-keepalive.C:
				cache.Status() // triggers refresh if TTL expired
				newGen := cache.Generation()
				if newGen != gen {
					gen = newGen
					msg, _ := json.Marshal(map[string]any{
						"event":      "refresh",
						"generation": gen,
					})
					if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
						return
					}
				} else {
					// Ping to keep connection alive through tunnel
					if err := conn.Ping(ctx); err != nil {
						return
					}
				}
			}
		}
	}
}

// Events serves GET /events — Server-Sent Events stream (legacy).
// Kept for backward compatibility. New clients should use /ws.
func Events(cache *collector.Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w, r)
		if r.Method == http.MethodOptions {
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		notify := cache.Subscribe()
		defer cache.Unsubscribe(notify)

		cache.Status()
		gen := cache.Generation()
		fmt.Fprintf(w, "event: connected\ndata: {\"generation\":%d}\n\n", gen)
		flusher.Flush()

		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()

		for {
			select {
			case <-r.Context().Done():
				return

			case <-notify:
				newGen := cache.Generation()
				if newGen != gen {
					gen = newGen
					fmt.Fprintf(w, "event: refresh\ndata: {\"generation\":%d}\n\n", gen)
					flusher.Flush()
				}

			case <-keepalive.C:
				cache.Status()
				newGen := cache.Generation()
				if newGen != gen {
					gen = newGen
					fmt.Fprintf(w, "event: refresh\ndata: {\"generation\":%d}\n\n", gen)
				} else {
					fmt.Fprint(w, ": keepalive\n\n")
				}
				flusher.Flush()
			}
		}
	}
}
