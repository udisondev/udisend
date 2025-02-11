// client.go
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/pion/webrtc/v4"
)

// SignalMessage используется для обмена SDP между клиентом и сервером.
type SignalMessage struct {
	SDP string `json:"sdp"`
}

var (
	nick         string
	signalServer string // например, "http://<public_ip>:8080/offer"
)

func sendOffer(offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	offerMsg := SignalMessage{SDP: offer.SDP}
	data, err := json.Marshal(offerMsg)
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	resp, err := http.Post(signalServer, "application/json", bytes.NewReader(data))
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	defer resp.Body.Close()
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	var answerMsg SignalMessage
	if err = json.Unmarshal(respData, &answerMsg); err != nil {
		return webrtc.SessionDescription{}, err
	}
	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerMsg.SDP,
	}
	return answer, nil
}

func main() {
	flag.StringVar(&nick, "nick", "anon", "Никнейм для клиента")
	flag.StringVar(&signalServer, "signal", "", "Адрес сигнального сервера (например, http://<public_ip>:8080/offer)")
	flag.Parse()
	if signalServer == "" {
		log.Fatal("Не задан адрес сигнального сервера (--signal)")
	}

	// Конфигурация ICE с STUN-сервером.
	iceServers := []webrtc.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: iceServers,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Создаем DataChannel.
	dc, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		log.Fatal(err)
	}
	dc.OnOpen(func() {
		log.Println("DataChannel открыт!")
		dc.SendText(fmt.Sprintf("Привет от %s!", nick))
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		log.Printf("Получено сообщение: %s", string(msg.Data))
	})

	// Создаем SDP offer.
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatal(err)
	}
	if err = pc.SetLocalDescription(offer); err != nil {
		log.Fatal(err)
	}

	// Ждем завершения ICE Gathering.
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete

	log.Println("Отправка SDP offer на сигнальный сервер...")
	answer, err := sendOffer(*pc.LocalDescription())
	if err != nil {
		log.Fatalf("Ошибка отправки offer: %v", err)
	}
	if err = pc.SetRemoteDescription(answer); err != nil {
		log.Fatalf("Ошибка установки remote description: %v", err)
	}
	log.Println("Соединение установлено! Теперь можно обмениваться сообщениями через DataChannel.")

	// Для демонстрации ждем некоторое время и затем отправляем сообщение.
	time.Sleep(5 * time.Second)
	if dc.ReadyState() == webrtc.DataChannelStateOpen {
		err = dc.SendText(fmt.Sprintf("Сообщение от %s", nick))
		if err != nil {
			log.Printf("Ошибка отправки сообщения: %v", err)
		}
	} else {
		log.Println("DataChannel не открыт")
	}

	// Оставляем приложение активным.
	select {}
}

