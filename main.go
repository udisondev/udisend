// server.go
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// MessageType обозначает тип сигнализационного сообщения.
type MessageType string

const (
	TypeOffer    MessageType = "offer"
	TypeAnswer   MessageType = "answer"
	TypeCandidate MessageType = "candidate"
)

// SignalMsg – универсальная структура сигнализации.
type SignalMsg struct {
	Type MessageType       `json:"type"`
	From string            `json:"from"`
	To   string            `json:"to,omitempty"` // если пусто – широковещательно
	SDP  string            `json:"sdp,omitempty"`
	// Кандидат может быть любым JSON объектом, здесь упрощённо строка
	Candidate string       `json:"candidate,omitempty"`
}

// Client представляет подключённого по WebSocket клиента.
type Client struct {
	ID   string
	Conn *websocket.Conn
}

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	clientsMu sync.Mutex
	clients   = make(map[string]*Client) // ключ – ID клиента (например, его ник)
)

func wsHandler(w http.ResponseWriter, r *http.Request) {
	// Обновляем соединение до WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	// Ожидаем, что первым сообщением клиент пришлёт свой ID (ник)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Println("Ошибка чтения id:", err)
		conn.Close()
		return
	}
	id := string(msg)
	client := &Client{ID: id, Conn: conn}

	clientsMu.Lock()
	clients[id] = client
	clientsMu.Unlock()
	log.Printf("Клиент %s подключился", id)

	// Запускаем обработку сообщений от клиента
	go handleClient(client)
}

func handleClient(client *Client) {
	defer func() {
		client.Conn.Close()
		clientsMu.Lock()
		delete(clients, client.ID)
		clientsMu.Unlock()
		log.Printf("Клиент %s отключился", client.ID)
	}()

	for {
		_, msg, err := client.Conn.ReadMessage()
		if err != nil {
			log.Println("Ошибка чтения сообщения:", err)
			return
		}
		var signal SignalMsg
		if err := json.Unmarshal(msg, &signal); err != nil {
			log.Println("Ошибка декодирования сигнала:", err)
			continue
		}
		signal.From = client.ID
		broadcast(signal, client.ID)
	}
}

// broadcast рассылает сообщение всем клиентам, кроме отправителя,
// либо, если To задан, только конкретному получателю.
func broadcast(signal SignalMsg, senderID string) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	data, err := json.Marshal(signal)
	if err != nil {
		log.Println("Ошибка маршалинга:", err)
		return
	}

	if signal.To != "" {
		if toClient, ok := clients[signal.To]; ok {
			toClient.Conn.WriteMessage(websocket.TextMessage, data)
		} else {
			log.Printf("Клиент %s не найден", signal.To)
		}
		return
	}

	// Рассылка всем, кроме отправителя.
	for id, c := range clients {
		if id == senderID {
			continue
		}
		c.Conn.WriteMessage(websocket.TextMessage, data)
	}
}

func main() {
	http.HandleFunc("/ws", wsHandler)
	log.Println("Сигнальный сервер запущен на :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

