package websocket

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/royaldevlopments/royalwings/internal/server"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Handler struct {
	token   string
	manager *server.Manager
	clients map[string]*Client
	mu      sync.RWMutex
}

type Client struct {
	conn       *websocket.Conn
	serverUUID string
	done       chan struct{}
	mu         sync.Mutex
}

func NewHandler(token string, manager *server.Manager) *Handler {
	return &Handler{
		token:   token,
		manager: manager,
		clients: make(map[string]*Client),
	}
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+h.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	serverUUID := r.URL.Query().Get("server")
	if serverUUID == "" {
		http.Error(w, "server parameter required", http.StatusBadRequest)
		return
	}

	srv, ok := h.manager.GetServer(serverUUID)
	if !ok {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed for %s: %v", serverUUID, err)
		return
	}

	client := &Client{
		conn:       conn,
		serverUUID: serverUUID,
		done:       make(chan struct{}),
	}

	h.mu.Lock()
	h.clients[serverUUID] = client
	h.mu.Unlock()

	log.Printf("WebSocket client connected for server %s", serverUUID)

	defer func() {
		h.mu.Lock()
		delete(h.clients, serverUUID)
		h.mu.Unlock()
		conn.Close()
		log.Printf("WebSocket client disconnected for server %s", serverUUID)
	}()

	go h.writeConsoleOutput(srv, client)

	h.readMessages(srv, client)
}

func (h *Handler) readMessages(srv *server.Server, client *Client) {
	defer close(client.done)

	for {
		_, message, err := client.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("WebSocket read error for %s: %v", client.serverUUID, err)
			}
			return
		}

		var msg struct {
			Event   string          `json:"event"`
			Data    json.RawMessage `json:"data"`
		}

		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		switch msg.Event {
		case "auth":
			continue
		case "send command":
			var cmdData struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(msg.Data, &cmdData); err != nil {
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := srv.SendCommand(ctx, cmdData.Command); err != nil {
				log.Printf("Failed to send command to %s: %v", client.serverUUID, err)
			}
			cancel()
		}
	}
}

func (h *Handler) writeConsoleOutput(srv *server.Server, client *Client) {
	if srv.GetState() != server.StateRunning {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	reader, writer, err := srv.Attach(ctx)
	if err != nil {
		log.Printf("Failed to attach to container %s: %v", client.serverUUID, err)
		return
	}
	defer reader.Close()
	defer writer.Close()

	buf := make([]byte, 4096)
	for {
		select {
		case <-client.done:
			return
		default:
			n, err := reader.Read(buf)
			if err != nil {
				return
			}

			msg, _ := json.Marshal(map[string]interface{}{
				"event": "console output",
				"data":  string(buf[:n]),
			})

			client.mu.Lock()
			if err := client.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				client.mu.Unlock()
				return
			}
			client.mu.Unlock()
		}
	}
}

func (h *Handler) BroadcastStatus(serverUUID string, status string) {
	h.mu.RLock()
	client, ok := h.clients[serverUUID]
	h.mu.RUnlock()

	if !ok {
		return
	}

	msg, _ := json.Marshal(map[string]interface{}{
		"event": "status",
		"data":  status,
	})

	client.mu.Lock()
	defer client.mu.Unlock()
	client.conn.WriteMessage(websocket.TextMessage, msg)
}
