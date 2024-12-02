package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Client struct {
	ID         string
	Username   string
	Connection *websocket.Conn
	Room       *Room
}

type Room struct {
	ID      string
	Clients map[string]*Client
}

type Message struct {
	Type     string          `json:"type"`
	From     string          `json:"from"`
	To       string          `json:"to,omitempty"`
	Username string          `json:"username,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

type SignalingServer struct {
	rooms map[string]*Room
	mu    sync.RWMutex
}

func NewSignalingServer() *SignalingServer {
	return &SignalingServer{
		rooms: make(map[string]*Room),
	}
}

func (s *SignalingServer) HandleWebSocket(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upgrade connection"})
		return
	}
	defer conn.Close()

	roomID := c.Query("roomId")
	clientID := c.Query("clientId")
	if roomID == "" || clientID == "" {
		log.Println("Missing roomId or clientId in request")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing roomId or clientId"})
		return
	}

	// Get or create room
	s.mu.Lock()
	room, exists := s.rooms[roomID]
	if !exists {
		room = &Room{
			ID:      roomID,
			Clients: make(map[string]*Client),
		}
		s.rooms[roomID] = room
		log.Printf("Created new room: %s", roomID)
	}
	s.mu.Unlock()

	// Create client
	client := &Client{
		ID:         clientID,
		Connection: conn,
		Room:       room,
	}

	// Add client to room
	s.mu.Lock()
	room.Clients[clientID] = client
	s.mu.Unlock()

	log.Printf("Client %s joined room %s", clientID, roomID)

	// Notify others of new peer
	s.mu.RLock()
	for id, c := range room.Clients {
		if id != clientID {
			msg := Message{
				Type:     "new-peer",
				From:     clientID,
				Username: client.Username,
			}
			if err := c.Connection.WriteJSON(msg); err != nil {
				log.Printf("Error notifying peer %s: %v", id, err)
			}
		}
	}
	s.mu.RUnlock()

	// Handle messages
	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("Read error from client %s: %v", clientID, err)
			break
		}

		msg.From = clientID

		// Handle signaling messages
		if msg.Type == "offer" || msg.Type == "answer" || msg.Type == "candidate" {
			s.mu.RLock()
			if msg.To != "" {
				if peer, ok := room.Clients[msg.To]; ok {
					if err := peer.Connection.WriteJSON(msg); err != nil {
						log.Printf("Error sending message to %s: %v", msg.To, err)
					}
				}
			}
			s.mu.RUnlock()
		}

		// Handle username updates
		if msg.Type == "join" {
			client.Username = msg.Username
			log.Printf("Client %s set username: %s", clientID, msg.Username)
		}
	}

	// Cleanup
	s.mu.Lock()
	delete(room.Clients, clientID)
	clientCount := len(room.Clients)
	s.mu.Unlock()

	// Notify others that peer left
	s.mu.RLock()
	for id, c := range room.Clients {
		if id != clientID {
			if err := c.Connection.WriteJSON(Message{
				Type: "peer-left",
				From: clientID,
			}); err != nil {
				log.Printf("Error notifying peer %s of disconnect: %v", id, err)
			}
		}
	}
	s.mu.RUnlock()

	// Remove empty room
	if clientCount == 0 {
		s.mu.Lock()
		delete(s.rooms, roomID)
		s.mu.Unlock()
		log.Printf("Room %s removed", roomID)
	}
}

func main() {
	// Set Gin to release mode
	gin.SetMode(gin.ReleaseMode)

	// Create signaling server
	server := NewSignalingServer()

	// Create Gin router
	r := gin.Default()

	// WebSocket route
	r.GET("/ws", server.HandleWebSocket)

	// Serve static files with a specific group
    r.GET("/", func(c *gin.Context) {
        c.File("./index.html")
    })
    r.Static("/static", "./static")

	// Start server
	log.Println("Server starting on :8080")
	r.Run(":8080")
}
