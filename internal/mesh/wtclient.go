// Package mesh — wtclient.go provides a WebTransport client for connecting
// agentd instances to meshd's WebTransport server.
//
// Sends agent status via datagrams (broadcast to all peers), receives
// broadcast signals (alerts, tempo), and can send structured messages
// via bidirectional streams.
//
// Session 100 — WebTransport prototype.
package mesh

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/quic-go/webtransport-go"
)

// WTClient connects to meshd's WebTransport endpoint.
type WTClient struct {
	agentID  string
	meshAddr string // e.g. "https://localhost:9081/mesh"
	logger   *slog.Logger

	mu      sync.RWMutex
	session *webtransport.Session
	dialer  webtransport.Dialer

	onDatagram func([]byte) // handler for incoming broadcast datagrams
}

// NewWTClient creates a WebTransport client for the given agent.
// meshAddr should include the full URL (e.g. "https://localhost:9081/mesh").
func NewWTClient(agentID, meshAddr string, logger *slog.Logger) *WTClient {
	// Load mkcert CA from the standard location
	pool, _ := x509.SystemCertPool()
	if pool == nil {
		pool = x509.NewCertPool()
	}
	home, _ := os.UserHomeDir()
	caFile := home + "/Library/Application Support/mkcert/rootCA.pem"
	if caCert, err := os.ReadFile(caFile); err == nil {
		pool.AppendCertsFromPEM(caCert)
	}

	return &WTClient{
		agentID:  agentID,
		meshAddr: meshAddr,
		logger:   logger,
		dialer: webtransport.Dialer{
			TLSClientConfig: &tls.Config{
				RootCAs:            pool,
				InsecureSkipVerify: true, // localhost dev — cert rotates every 14 days
			},
		},
	}
}

// OnDatagram sets the handler for incoming broadcast datagrams.
func (c *WTClient) OnDatagram(h func([]byte)) { c.onDatagram = h }

// Connect establishes a WebTransport session with meshd.
// Blocks until the context cancels, reconnecting on failure.
func (c *WTClient) Connect(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.connectOnce(ctx)
		if err != nil && ctx.Err() == nil {
			c.logger.Warn("webtransport connection failed, retrying", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (c *WTClient) connectOnce(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, session, err := c.dialer.Dial(dialCtx, c.meshAddr, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	c.mu.Lock()
	c.session = session
	c.mu.Unlock()

	c.logger.Info("webtransport connected to meshd", "addr", c.meshAddr)

	// Send identity on first bidirectional stream
	stream, err := session.OpenStream()
	if err != nil {
		return fmt.Errorf("open identity stream: %w", err)
	}
	json.NewEncoder(stream).Encode(map[string]string{
		"agent_id": c.agentID,
		"type":     "agent",
	})
	// Read welcome
	var welcome map[string]any
	json.NewDecoder(stream).Decode(&welcome)
	stream.Close()
	c.logger.Info("webtransport identity confirmed", "welcome", welcome)

	// Listen for incoming datagrams (broadcast from meshd)
	go c.readDatagrams(session)

	// Block until session closes
	<-session.Context().Done()

	c.mu.Lock()
	c.session = nil
	c.mu.Unlock()
	c.logger.Info("webtransport session closed")

	return nil
}

func (c *WTClient) readDatagrams(session *webtransport.Session) {
	for {
		data, err := session.ReceiveDatagram(session.Context())
		if err != nil {
			return
		}
		if c.onDatagram != nil {
			c.onDatagram(data)
		}
	}
}

// SendDatagram sends a datagram to meshd for broadcast.
func (c *WTClient) SendDatagram(data []byte) error {
	c.mu.RLock()
	s := c.session
	c.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("not connected")
	}
	return s.SendDatagram(data)
}

// SendStatus broadcasts an agent status update as a datagram.
func (c *WTClient) SendStatus(status any) error {
	data, err := json.Marshal(map[string]any{
		"type":     "status",
		"agent_id": c.agentID,
		"data":     status,
	})
	if err != nil {
		return err
	}
	return c.SendDatagram(data)
}

// SendMessage sends a structured interagent/v1 message via a bidirectional stream.
// Returns the server's acknowledgment.
func (c *WTClient) SendMessage(ctx context.Context, msg any) (map[string]string, error) {
	c.mu.RLock()
	s := c.session
	c.mu.RUnlock()
	if s == nil {
		return nil, fmt.Errorf("not connected")
	}

	stream, err := s.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	if err := json.NewEncoder(stream).Encode(msg); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	var ack map[string]string
	if err := json.NewDecoder(stream).Decode(&ack); err != nil {
		return nil, fmt.Errorf("decode ack: %w", err)
	}
	return ack, nil
}

// Connected reports whether the client has an active session.
func (c *WTClient) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.session != nil
}
