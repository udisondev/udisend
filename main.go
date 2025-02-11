// signaling_server.go
package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/pion/webrtc/v4"
)

type SignalMessage struct {
	SDP string `json:"sdp"`
}

func offerHandler(w http.ResponseWriter, r *http.Request) {
	// Распаковка SDP offer от клиента.
	var offerMsg SignalMessage
	if err := json.NewDecoder(r.Body).Decode(&offerMsg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("Получен SDP offer:\n%s", offerMsg.SDP)

	// Создаем PeerConnection (с STUN для ICE).
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Опционально: создаем DataChannel (если нужно).
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("DataChannel создан: %s (ID %d)", dc.Label(), dc.ID())
		dc.OnOpen(func() {
			log.Printf("DataChannel %s открыт", dc.Label())
			dc.SendText("Привет от сигнального сервера!")
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Сообщение от %s: %s", dc.Label(), string(msg.Data))
		})
	})

	// Устанавливаем удаленное описание (offer).
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerMsg.SDP,
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Создаем SDP answer.
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Устанавливаем локальное описание.
	if err := pc.SetLocalDescription(answer); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Ждем завершения ICE gathering.
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete

	// Отправляем ответ.
	respMsg := SignalMessage{
		SDP: pc.LocalDescription().SDP,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(respMsg); err != nil {
		log.Println("Ошибка отправки ответа:", err)
	}
	log.Printf("Отправлен SDP answer:\n%s", respMsg.SDP)
}

func main() {
	http.HandleFunc("/offer", offerHandler)
	log.Println("Сигнальный сервер запущен на :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

