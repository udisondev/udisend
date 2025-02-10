package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/pion/stun"
)

// Глобальные переменные и мапы.
var (
	nick         string
	port         string
	myAddress    string
	parent       string
	localAddress string

	// peersMap хранит активные соединения: ключ – уникальный идентификатор (PeerID).
	addressPeerMap   = make(map[string]*Peer)
	addrPeerMapMutex = sync.Mutex{}
	nickPeerMap      = make(map[string]*Peer)
	nickPeerMapMutex = sync.Mutex{}

	// processedMessages предотвращает повторную обработку одного и того же сообщения.
	processedMessages = struct {
		m map[string]bool
		sync.Mutex
	}{m: make(map[string]bool)}

	// attemptedCandidates предотвращает повторные попытки подключения к одному и тому же кандидату.
	attemptedCandidates = struct {
		m map[string]bool
		sync.Mutex
	}{m: make(map[string]bool)}

	// Ограничение дополнительных подключений (помимо основного).
	maxAdditionalConnections = 2
)

// LogWriter перехватывает лог-сообщения и отправляет их в консоль.
type LogWriter struct{}

func (w *LogWriter) Write(p []byte) (n int, err error) {
	msg := string(p)
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	// Выводим лог только один раз через displayMessage
	displayMessage("[LOG] " + msg)
	return len(p), nil
}

// websocket upgrader для преобразования HTTP в WebSocket.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	flag.StringVar(&nick, "nick", "anon", "Никнейм для узла")
	flag.StringVar(&port, "port", "8000", "Порт для прослушивания")
	flag.StringVar(&myAddress, "address", "127.0.0.1", "Адрес для прослушивания")
	flag.StringVar(&parent, "parent", "", "Адрес родительского узла (IP:PORT)")
	flag.Parse()

	// Перенаправляем вывод логов так, чтобы они отображались в консоли.
	log.SetOutput(&LogWriter{})

	// Задаём локальный адрес (для тестирования на локальной машине).
	localAddress = myAddress + port

	go func() {
		for {
			<-time.After(10 * time.Second)
			ping()
		}
	}()

	// Запускаем сервер для входящих соединений.
	go startServer()

	// Если указан родительский узел, инициируем подключение.
	if parent != "" {
		go connectToPeer(parent)
	}

	// Запускаем цикл ввода пользователя.
	go inputLoop()

	// Блокируем main, чтобы приложение не завершалось.
	select {}
}

// inputLoop читает строки из stdin и отправляет их как чат-сообщения.
func inputLoop() {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Введите сообщение (для личного сообщения используйте формат: /recipient сообщение):")
	for scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		recipient := ""
		text := input
		if strings.HasPrefix(input, "/") {
			parts := strings.SplitN(input, " ", 2)
			if len(parts) == 2 {
				recipient = strings.TrimPrefix(parts[0], "/")
				text = parts[1]
			}
		}
		msg := Message{
			ID:        generateMessageID(),
			Sender:    nick,
			Recipient: recipient,
			Text:      text,
			HopPath:   []string{nick},
		}
		sendChatMessage(msg)
	}
	if err := scanner.Err(); err != nil {
		log.Println("Ошибка чтения ввода:", err)
	}
}

// startServer поднимает HTTP-сервер и обрабатывает путь /ws для входящих WebSocket-соединений.
func startServer() {
	http.HandleFunc("/ws", wsHandler)
	log.Printf("[%s] Сервер слушает на %s", nick, port)
	log.Fatal(http.ListenAndServe(myAddress+":"+port, nil))
}

// wsHandler обрабатывает входящие WebSocket-соединения, создаёт yamux-сессию и временно сохраняет соединение.
func wsHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Получен запрос от %s", r.RemoteAddr)
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Ошибка Upgrade:", err)
		return
	}
	conn := websocketToConn(ws)
	session, err := yamux.Server(conn, nil)
	if err != nil {
		log.Println("Ошибка создания yamux-сервера:", err)
		return
	}

	addrPeerMapMutex.Lock()
	addressPeerMap[r.RemoteAddr] = &Peer{session: session}
	addrPeerMapMutex.Unlock()

	go acceptStreams(session, r.RemoteAddr)
	sendCandidates(session, r.RemoteAddr)
}

// connectToPeer устанавливает исходящее соединение к заданному адресу.
func connectToPeer(address string) bool {
	uri := "ws://" + address + "/ws"
	log.Printf("connectToPeer: [%s] Подключение к %s", nick, address)
	ws, _, err := websocket.DefaultDialer.Dial(uri, nil)
	if err != nil {
		log.Println("connectToPeer: Ошибка Dial:", err)
		return false
	}
	conn := websocketToConn(ws)
	session, err := yamux.Client(conn, nil)
	if err != nil {
		log.Println("connectToPeer: Ошибка создания yamux клиента:", err)
		return false
	}
	// Сохраняем соединение временно по адресу до handshake.
	addrPeerMapMutex.Lock()
	addressPeerMap[address] = &Peer{session: session, Member: &Member{Address: address}}
	addrPeerMapMutex.Unlock()
	log.Printf("connectToPeer: Добавлен peer addr: %s", address)

	go acceptStreams(session, address)
	sendHello(session)
	return true
}

// acceptStreams принимает новые потоки из yamux-сессии.
func acceptStreams(session *yamux.Session, addr string) {
	for {
		stream, err := session.Accept()
		log.Printf("acceptStreams: Установлен новый stream c addr=%s", addr)
		if err != nil {
			log.Printf("acceptStreams: Ошибка Accept потока от %s: %+v", addr, err)
			delete(nickPeerMap, addressPeerMap[addr].Nick)
			delete(addressPeerMap, addr)
			return
		}
		go handleStream(stream, addr)
	}
}

// handleStream читает Envelope из потока и вызывает соответствующую обработку.
func handleStream(stream net.Conn, addr string) {
	defer stream.Close()
	decoder := json.NewDecoder(stream)
	var env Envelope
	if err := decoder.Decode(&env); err != nil {
		log.Printf("handleStream: Ошибка чтения envelope от %s: %+v", addr, err)
		return
	}
	switch env.Type {
	case "ping":
		encoder := json.NewEncoder(stream)
		err := encoder.Encode(Envelope{
			Type: "pong",
		})
		if err != nil {
			log.Printf("handleStream: Ошибка отправки pong")
		}
	case "pong":
	case "hello":
		handleHelloEnvelope(env.Data, addr)
	case "candidate":
		handleCandidateEnvelope(env.Data, addr)
	case "chat":
		var msg Message
		if err := json.Unmarshal(env.Data, &msg); err != nil {
			log.Printf("Ошибка декодирования chat-сообщения от %s: %+v", addr, err)
			return
		}
		processChatMessage(msg, addr)
	default:
		log.Printf("Неизвестный тип envelope от %s: %s", addr, env.Type)
	}
}

// handleHelloEnvelope обрабатывает handshake-сообщение и обновляет информацию о соединении.
func handleHelloEnvelope(data json.RawMessage, addr string) {
	var hello Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		log.Printf("handleHelloEnvelope: Ошибка декодирования hello от %s: %+v", addr, err)
		return
	}
	peer, ok := addressPeerMap[addr]
	if !ok {
		log.Printf("handleHelloEnvelope: Unknown hello from: %s", addr)
		return
	}
	memb := peer.Member
	memb.Nick = hello.Nick
	memb.PublicAddresses = hello.PublicAddresses
	log.Printf("handleHelloEnvelope: Получен hello от member= %+v", memb)
	nickPeerMapMutex.Lock()
	defer nickPeerMapMutex.Unlock()
	if peer, exists := addressPeerMap[addr]; exists {
		nickPeerMap[memb.Nick] = peer
	} else {
		log.Printf("Peer not found by addr: %s", addr)
	}
}

// handleCandidateEnvelope обрабатывает сообщение с кандидатами и инициирует подключение к новым узлам,
// если не достигнут лимит дополнительных соединений и кандидат ещё не пытались подключить.
func handleCandidateEnvelope(data json.RawMessage, senderNick string) {
	var candMsg CandidateMessage
	if err := json.Unmarshal(data, &candMsg); err != nil {
		log.Printf("handleCandidateEnvelope: Ошибка декодирования candidate message от %s: %+v", senderNick, err)
		return
	}
	for _, cand := range candMsg.Candidates {
		if _, ok := nickPeerMap[cand.Nick]; ok {
			continue
		}
		log.Printf("handleCandidateEnvelope: %s прислал кандидата для подключения <Candidate:%+v>", senderNick, cand)

		for _, addr := range cand.Addresses {
			log.Printf("Попытка установить соединение с nick=%s по addr=%s", senderNick, addr)
			if connectToPeer(addr) {
				log.Printf("handleCandidateEnvelope: Соединение с nick=%s установлено по addr=%s", cand.Nick, addr)
				break
			}
		}
		log.Printf("handleCandidateEnvelope: Соединение с nick=%s не установлено ни по одному из адресов", senderNick)

	}
}

// processChatMessage обрабатывает входящее чат-сообщение: выводит его и ретранслирует.
func processChatMessage(msg Message, from string) {
	processedMessages.Lock()
	if processedMessages.m[msg.ID] {
		processedMessages.Unlock()
		return
	}
	processedMessages.m[msg.ID] = true
	processedMessages.Unlock()

	if msg.Sender == nick {
		return
	}

	if msg.Recipient == nick {
		fmt.Printf("[ЛС] %s: %s\n", msg.Sender, msg.Text)
	} else if msg.Recipient == "" {
		fmt.Printf("%s: %s\n", msg.Sender, msg.Text)
	}

	// Ретранслируем сообщение всем, кроме источника.
	forwardMessage(msg, from)
}

// forwardMessage пересылает сообщение через все активные соединения (кроме источника).
func forwardMessage(msg Message, from string) {
	for nick, peer := range nickPeerMap {
		if nick == from {
			continue
		}
		if err := sendEnvelope(peer.session, "chat", msg); err != nil {
			log.Printf("Ошибка ретрансляции через %s: %+v", nick, err)
		}
	}
}

// sendEnvelope открывает новый поток в сессии и отправляет в него JSON-объект (envelope).
func sendEnvelope(session *yamux.Session, msgType string, payload interface{}) error {
	stream, err := session.Open()
	if err != nil {
		return err
	}
	defer stream.Close()
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{
		Type: msgType,
		Data: data,
	}
	encoder := json.NewEncoder(stream)
	return encoder.Encode(env)
}

// sendHello отправляет handshake-сообщение с информацией о данном узле.
func sendHello(session *yamux.Session) {
	publicAddresses, err := getPublicAddress()
	log.Printf("Обнаружены публичные IP: %v", publicAddresses)

	hello := Hello{
		Nick:            nick,
		Port:            port,
		PublicAddresses: publicAddresses,
	}
	if err != nil {
		log.Printf("Ошибка получения публичного адреса через STUN: %v", err)
		// Если STUN не сработал, используем fallback (локальный адрес)
		publicAddresses = []string{localAddress}
	}
	if err := sendEnvelope(session, "hello", hello); err != nil {
		log.Printf("Ошибка отправки hello: %+v", err)
	}
}

// sendCandidates отправляет список известных кандидатов (не более maxAdditionalConnections) через данную сессию.
func sendCandidates(session *yamux.Session, recipientNick string) {
	nickPeerMapMutex.Lock()
	var candidates []Candidate
	count := 0
	for nick, peer := range nickPeerMap {
		if recipientNick == nick {
			continue
		}
		if count >= maxAdditionalConnections {
			break
		}
		candidates = append(candidates, Candidate{
			Nick:      nick,
			Addresses: peer.PublicAddresses,
		})
		count++
	}
	nickPeerMapMutex.Unlock()
	candMsg := CandidateMessage{Candidates: candidates}
	if err := sendEnvelope(session, "candidate", candMsg); err != nil {
		log.Printf("Ошибка отправки candidates: %+v", err)
	}
}

// generateMessageID генерирует уникальный ID для сообщения.
func generateMessageID() string {
	return fmt.Sprintf("%s-%d", nick, time.Now().UnixNano())
}

// contains проверяет наличие строки в срезе.
func contains(arr []string, s string) bool {
	for _, item := range arr {
		if item == s {
			return true
		}
	}
	return false
}

// displayMessage выводит сообщение в консоль.
func displayMessage(text string) {
	fmt.Println(text)
}

// sendChatMessage отправляет сообщение всем подключённым узлам.
func sendChatMessage(msg Message) {
	for peerID, peer := range nickPeerMap {
		if err := sendEnvelope(peer.session, "chat", msg); err != nil {
			log.Printf("Ошибка отправки сообщения через %s: %+v", peerID, err)
		}
	}
}

// Обёртка WebSocket -> net.Conn (упрощённая для демонстрации)
type websocketConn struct {
	ws *websocket.Conn
}

func (w *websocketConn) Read(b []byte) (int, error) {
	_, r, err := w.ws.NextReader()
	if err != nil {
		return 0, err
	}
	return r.Read(b)
}

func (w *websocketConn) Write(b []byte) (int, error) {
	writer, err := w.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, err := writer.Write(b)
	if err != nil {
		return n, err
	}
	err = writer.Close()
	return n, err
}

func (w *websocketConn) Close() error {
	return w.ws.Close()
}

func (w *websocketConn) LocalAddr() net.Addr {
	return &net.TCPAddr{}
}

func (w *websocketConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{}
}

func (w *websocketConn) SetDeadline(t time.Time) error {
	return nil
}

func (w *websocketConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (w *websocketConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func websocketToConn(ws *websocket.Conn) net.Conn {
	return &websocketConn{ws: ws}
}

func ping() {
	for _, peer := range nickPeerMap {
		sendEnvelope(peer.session, "ping", nil)
	}
}

func getPublicAddress() ([]string, error) {
	toReturn := make(map[string]struct{})
	ips := sync.Map{}
	wg := sync.WaitGroup{}
	for _, ss := range stunServers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			conn, err := net.Dial("udp", ss)
			if err != nil {
				return
			}
			defer conn.Close()

			// Создаем STUN-клиента.
			client, err := stun.NewClient(conn)
			if err != nil {
				return
			}
			defer client.Close()

			var xorAddr stun.XORMappedAddress
			err = client.Do(stun.MustBuild(stun.TransactionID, stun.BindingRequest), func(res stun.Event) {
				if res.Error != nil {
					err = res.Error
					return
				}
				// Извлекаем XOR‑mapped адрес из ответа.
				if getErr := xorAddr.GetFrom(res.Message); getErr != nil {
					err = getErr
					return
				}
			})
			if err != nil {
				return
			}
			ips.Store(fmt.Sprintf("%s:%d", xorAddr.IP, xorAddr.Port), struct{}{})
		}()
	}

	wg.Wait()

	ips.Range(func(key, _ any) bool {
		toReturn[key.(string)] = struct{}{}
		return true
	})

	return slices.Collect(maps.Keys(toReturn)), nil
}
