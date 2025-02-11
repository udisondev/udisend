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

// SignalMsg и MessageType определены также, как на сервере.
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
	signalServer string
	remotePeer   string // если задан, будет направлено сообщение только конкретному клиенту
)

func main() {
	flag.StringVar(&nick, "nick", "anon", "Никнейм клиента")
	flag.StringVar(&signalServer, "signal", "", "Адрес сигнального сервера, например ws://<public_ip>:8080/ws")
	flag.StringVar(&remotePeer, "to", "", "Если задан, будет направлено сообщение конкретному клиенту")
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

	// Отправляем свой идентификатор (ник) сразу после подключения.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(nick)); err != nil {
		log.Fatal("Ошибка отправки идентификатора:", err)
	}

	// Создаем PeerConnection с ICE-серверами.
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Если клиент получает DataChannel от удаленного узла, обработчик OnDataChannel.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Получен DataChannel: %s", dc.Label())
		dc.OnOpen(func() {
			log.Println("DataChannel открыт!")
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Получено сообщение: %s", string(msg.Data))
		})
	})

	// Если клиент сам хочет создать DataChannel (например, инициатор), то:
	var dc *webrtc.DataChannel
	// Если remotePeer не задан, считаем себя инициатором.
	if remotePeer == "" {
		dc, err = pc.CreateDataChannel("data", nil)
		if err != nil {
			log.Fatal(err)
		}
		dc.OnOpen(func() {
			log.Println("DataChannel открыт!")
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Получено сообщение: %s", string(msg.Data))
		})
	}

	// Обработка входящих сигналов из WebSocket в отдельной горутине.
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
			handleSignal(sig, pc)
		}
	}()

	// Если мы инициатор, создаем offer и отправляем его.
	if remotePeer == "" {
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

		offerMsg := SignalMsg{
			Type: TypeOffer,
			SDP:  pc.LocalDescription().SDP,
		}
		sendSignal(conn, offerMsg)
	}

	// Читаем команды из stdin для отправки сообщений через DataChannel.
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			text := scanner.Text()
			if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
				dc.SendText(text)
			} else {
				log.Println("DataChannel не открыт")
			}
		}
	}()

	// Блокируем main.
	select {}
}

func sendSignal(conn *websocket.Conn, sig SignalMsg) {
	// Если remotePeer задан, добавляем его в поле To.
	if remotePeer != "" {
		sig.To = remotePeer
	}
	data, err := json.Marshal(sig)
	if err != nil {
		log.Println("Ошибка маршалинга сигнала:", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Println("Ошибка отправки сигнала:", err)
	}
}

func handleSignal(sig SignalMsg, pc *webrtc.PeerConnection) {
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
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			log.Println("Ошибка создания answer:", err)
			return
		}
		if err = pc.SetLocalDescription(answer); err != nil {
			log.Println("Ошибка установки локального описания:", err)
			return
		}
		// Ждем ICE Gathering.
		gatherComplete := webrtc.GatheringCompletePromise(pc)
		<-gatherComplete
		answerMsg := SignalMsg{
			Type: TypeAnswer,
			SDP:  pc.LocalDescription().SDP,
		}
		// Отправляем answer через сигнальный канал.
		// (Если требуется, можно добавить поле To, чтобы направить его конкретно отправителю.)
		// Здесь мы посылаем широковещательно.
		sendSignalConn(answerMsg)
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
		// Для простоты здесь не реализована обработка ICE кандидатов отдельно.
		log.Println("Получен кандидат:", sig.Candidate)
	default:
		log.Println("Неизвестный тип сигнала:", sig.Type)
	}
}

// sendSignalConn отправляет сигнал через глобальное соединение к сигнальному серверу.
// В этом упрощенном примере мы просто используем стандартное подключение, которое было создано в main.
func sendSignalConn(sig SignalMsg) {
	// Для демонстрации этот метод можно реализовать, если вы храните глобальное подключение.
	// Здесь этот метод не реализован полностью.
}

