// server.go
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type MessageType string

const (
	TypeOffer     MessageType = "offer"
	TypeAnswer    MessageType = "answer"
	TypeCandidate MessageType = "candidate"
)

// SignalMsg — универсальная структура сигнализации.
type SignalMsg struct {
	Type      MessageType `json:"type"`
	From      string      `json:"from"`
	To        string      `json:"to,omitempty"` // если пусто — широковещательно
	SDP       string      `json:"sdp,omitempty"`
	Candidate string      `json:"candidate,omitempty"`
}

type Client struct {
	ID   string
	Conn *websocket.Conn
}

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	clientsMu sync.Mutex
	clients   = make(map[string]*Client) // ключ — идентификатор (например, ник)
)

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}
	// Ожидаем, что первым сообщением клиент отправит свой ник.
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Println("Ошибка чтения ID:", err)
		conn.Close()
		return
	}
	id := string(msg)
	client := &Client{ID: id, Conn: conn}

	clientsMu.Lock()
	clients[id] = client
	clientsMu.Unlock()
	log.Printf("Клиент %s подключился", id)

	// Читаем сообщения от клиента.
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Клиент %s отключился: %v", id, err)
			break
		}
		var sig SignalMsg
		if err := json.Unmarshal(message, &sig); err != nil {
			log.Println("Ошибка декодирования сигнала:", err)
			continue
		}
		sig.From = id
		routeMessage(sig)
	}
	conn.Close()
	clientsMu.Lock()
	delete(clients, id)
	clientsMu.Unlock()
	log.Printf("Клиент %s отключился", id)
}

func routeMessage(sig SignalMsg) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	data, err := json.Marshal(sig)
	if err != nil {
		log.Println("Ошибка маршалинга сигнала:", err)
		return
	}

	// Если поле To задано, отправляем только указанному клиенту.
	if sig.To != "" {
		if target, ok := clients[sig.To]; ok {
			target.Conn.WriteMessage(websocket.TextMessage, data)
			log.Printf("Сообщение от %s отправлено клиенту %s", sig.From, sig.To)
		} else {
			log.Printf("Клиент %s не найден", sig.To)
		}
		return
	}

	// Иначе рассылаем всем, кроме отправителя.
	for id, client := range clients {
		if id == sig.From {
			continue
		}
		client.Conn.WriteMessage(websocket.TextMessage, data)
	}
	log.Printf("Сообщение от %s отправлено всем", sig.From)
}

func main() {
	http.HandleFunc("/ws", wsHandler)
	log.Println("Сигнальный сервер запущен на :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

