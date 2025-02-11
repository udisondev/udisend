// server.go
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
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
