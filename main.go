// client.go
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
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
	signalServer string // например, ws://<public_ip>:8080/ws
)

var dataChannel *webrtc.DataChannel

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

	// Отправляем свой ник.
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

	// Обработка входящих DataChannel.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Получен DataChannel: %s", dc.Label())
		dataChannel = dc
		dc.OnOpen(func() {
			log.Println("DataChannel открыт!")
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			// Здесь логируем, что сообщение получено напрямую от другого узла.
			log.Printf("Прямое сообщение получено: %s", string(msg.Data))
		})
	})

	// Обработка входящих сигналов через WebSocket.
	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Println("Ошибка чтения сигнала:", err)
				return
			}
			var sig SignalMsg
			if err := json.Unmarshal(message, &sig); err != nil {
				log.Println("Ошибка декодирования сигнала:", err)
				continue
			}
			handleSignal(sig, pc, conn)
		}
	}()

	// Если роль offerer, создаем DataChannel и генерируем offer.
	if role == "offerer" {
		dc, err := pc.CreateDataChannel("data", nil)
		if err != nil {
			log.Fatal(err)
		}
		dataChannel = dc
		dc.OnOpen(func() {
			log.Println("DataChannel открыт!")
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Прямое сообщение получено: %s", string(msg.Data))
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

	// Запускаем ввод из консоли: отправка сообщений через DataChannel.
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		fmt.Println("Введите сообщение:")
		for scanner.Scan() {
			text := scanner.Text()
			if dataChannel == nil || dataChannel.ReadyState() != webrtc.DataChannelStateOpen {
				log.Println("DataChannel не открыт, сообщение не отправлено")
				continue
			}
			// Отправляем сообщение с префиксом, чтобы можно было видеть, кто отправил.
			messageToSend := fmt.Sprintf("%s: %s", nick, text)
			err := dataChannel.SendText(messageToSend)
			if err != nil {
				log.Printf("Ошибка отправки сообщения: %v", err)
			} else {
				log.Printf("Прямое сообщение отправлено: %s", messageToSend)
			}
		}
		if err := scanner.Err(); err != nil {
			log.Println("Ошибка чтения ввода:", err)
		}
	}()

	// Оставляем приложение активным.
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
		// Если мы не инициатор, создаем answer.
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
		// Обработка ICE кандидатов не реализована отдельно в этом примере.
		log.Printf("Получен кандидат: %s", sig.Candidate)
	default:
		log.Printf("Неизвестный тип сигнала: %s", sig.Type)
	}
}
