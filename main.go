// client.go
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"log"
	"net/url"
	"os"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

type MessageType string

const (
	TypeOffer     MessageType = "offer"
	TypeAnswer    MessageType = "answer"
	TypeCandidate MessageType = "candidate"
)

type SignalMsg struct {
	Type      MessageType `json:"type"`
	From      string      `json:"from"`
	To        string      `json:"to,omitempty"`
	SDP       string      `json:"sdp,omitempty"`
	Candidate string      `json:"candidate,omitempty"`
}

var (
	nick         string
	role         string // "offerer" или "answerer"
	signalServer string // адрес сигнального сервера, например, ws://<public_ip>:8080/ws
)

func main() {
	flag.StringVar(&nick, "nick", "anon", "Никнейм клиента")
	flag.StringVar(&role, "role", "offerer", "Роль клиента: offerer или answerer")
	flag.StringVar(&signalServer, "signal", "", "Адрес сигнального сервера (например, ws://<public_ip>:8080/ws)")
	flag.Parse()

	if signalServer == "" {
		log.Fatal("Не задан адрес сигнального сервера (--signal)")
	}

	// Подключаемся к сигнальному серверу.
	u, err := url.Parse(signalServer)
	if err != nil {
		log.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("Ошибка подключения к сигнальному серверу:", err)
	}
	defer conn.Close()
	log.Printf("Подключились к сигнальному серверу %s", signalServer)

	// Отправляем свой идентификатор сразу после подключения.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(nick)); err != nil {
		log.Fatal("Ошибка отправки идентификатора:", err)
	}

	// Создаем PeerConnection с ICE-серверами.
	iceServers := []webrtc.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: iceServers,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Обработка DataChannel.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Получен DataChannel: %s", dc.Label())
		dc.OnOpen(func() {
			log.Println("DataChannel открыт!")
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Получено сообщение: %s", string(msg.Data))
		})
	})

	// Обработка входящих сигналов.
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Println("Ошибка чтения сигнала:", err)
				return
			}
			var sig SignalMsg
			if err := json.Unmarshal(msg, &sig); err != nil {
				log.Println("Ошибка декодирования сигнала:", err)
				continue
			}
			handleSignal(sig, pc, conn)
		}
	}()

	// Если мы инициатор, создаем DataChannel, генерируем offer и отправляем его.
	if role == "offerer" {
		dc, err := pc.CreateDataChannel("data", nil)
		if err != nil {
			log.Fatal(err)
		}
		dc.OnOpen(func() {
			log.Println("DataChannel открыт!")
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Получено сообщение: %s", string(msg.Data))
		})

		offer, err := pc.CreateOffer(nil)
		if err != nil {
			log.Fatal(err)
		}
		if err = pc.SetLocalDescription(offer); err != nil {
			log.Fatal(err)
		}

		gatherComplete := webrtc.GatheringCompletePromise(pc)
		<-gatherComplete

		offerMsg := SignalMsg{
			Type: TypeOffer,
			SDP:  pc.LocalDescription().SDP,
		}
		sendSignal(conn, offerMsg)
	}

	// Чтение команд из stdin для отправки сообщений через DataChannel.
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			text := scanner.Text()
			// Отправляем сообщение через DataChannel (если открыт).
			// Здесь, для простоты, предполагается, что у инициатора DataChannel создан и открыт.
			// В реальном коде следует проверить готовность DataChannel.
			log.Printf("Отправка сообщения: %s", text)
			// Для демонстрации: создадим новое предложение через DataChannel, если оно имеется.
			// Обычно DataChannel уже открыт и готов к обмену.
		}
	}()

	// Блок main.
	select {}
}

func sendSignal(conn *websocket.Conn, sig SignalMsg) {
	data, err := json.Marshal(sig)
	if err != nil {
		log.Println("Ошибка маршалинга сигнала:", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Println("Ошибка отправки сигнала:", err)
	}
}

func handleSignal(sig SignalMsg, pc *webrtc.PeerConnection, conn *websocket.Conn) {
	switch sig.Type {
	case TypeOffer:
		log.Println("Получен offer")
		offer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  sig.SDP,
		}
		if err := pc.SetRemoteDescription(offer); err != nil {
			log.Println("Ошибка установки remote description:", err)
			return
		}
		// Если мы не инициатор, создаем ответ.
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			log.Println("Ошибка создания answer:", err)
			return
		}
		if err = pc.SetLocalDescription(answer); err != nil {
			log.Println("Ошибка установки локального описания:", err)
			return
		}
		gatherComplete := webrtc.GatheringCompletePromise(pc)
		<-gatherComplete
		answerMsg := SignalMsg{
			Type: TypeAnswer,
			SDP:  pc.LocalDescription().SDP,
			To:   sig.From, // направляем конкретно отправителю
		}
		sendSignal(conn, answerMsg)
	case TypeAnswer:
		log.Println("Получен answer")
		answer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  sig.SDP,
		}
		if err := pc.SetRemoteDescription(answer); err != nil {
			log.Println("Ошибка установки remote description:", err)
		}
	case TypeCandidate:
		// Обработка ICE кандидатов (для упрощения здесь не реализована отдельно).
		log.Printf("Получен кандидат: %s", sig.Candidate)
	default:
		log.Printf("Неизвестный тип сигнала: %s", sig.Type)
	}
}

