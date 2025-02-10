package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

// Envelope – универсальная обёртка для всех сообщений.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Message – структура для чат-сообщений.
type Message struct {
	ID        string   `json:"id"`
	Sender    string   `json:"sender"`
	Recipient string   `json:"recipient"` // Если пусто – широковещательное сообщение.
	Text      string   `json:"text"`
	HopPath   []string `json:"hop_path"` // Для предотвращения циклической ретрансляции.
}

// Hello – handshake-сообщение, передаёт уникальный идентификатор и публичный (слушающий) адрес.
type Hello struct {
	Nick string `json:"peer_id"`
	Port string `json:"port"`
}

// Candidate – информация об узле для рекурсивного подключения.
type Candidate struct {
	Nick    string `json:"peer_id"`
	Address string `json:"address"`
}

type Member struct {
	Nick          string
	Address       string
	PublicAddress string
}

// CandidateMessage – сообщение, содержащее список кандидатов.
type CandidateMessage struct {
	Candidates []Candidate `json:"candidates"`
}

// Peer – информация о подключённом узле.
type Peer struct {
	session *yamux.Session
	*Member
}

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
		fmt.Printf("You: %s\n", text)
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
	addressPeerMap[r.RemoteAddr] = &Peer{session: session, Member: &Member{Nick: "", Address: r.RemoteAddr}}
	addrPeerMapMutex.Unlock()

	go acceptStreams(session, r.RemoteAddr)
	// Отправляем кандидатов и hello-сообщение.
	sendCandidates(session, r.RemoteAddr)
	sendHello(session)
}

// connectToPeer устанавливает исходящее соединение к заданному адресу.
func connectToPeer(address string) {
	uri := "ws://" + address + "/ws"
	log.Printf("[%s] Подключение к %s", nick, address)
	ws, _, err := websocket.DefaultDialer.Dial(uri, nil)
	if err != nil {
		log.Println("Ошибка Dial:", err)
		return
	}
	conn := websocketToConn(ws)
	session, err := yamux.Client(conn, nil)
	if err != nil {
		log.Println("Ошибка создания yamux клиента:", err)
		return
	}
	// Сохраняем соединение временно по адресу до handshake.
	addrPeerMapMutex.Lock()
	addressPeerMap[address] = &Peer{session: session, Member: &Member{Address: address}}
	addrPeerMapMutex.Unlock()
	log.Printf("Добавлен peer addr: %s", address)

	go acceptStreams(session, address)
	sendHello(session)
}

// acceptStreams принимает новые потоки из yamux-сессии.
func acceptStreams(session *yamux.Session, addr string) {
	for {
		stream, err := session.Accept()
		if err != nil {
			log.Printf("Ошибка Accept потока от %s: %+v", addr, err)
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
		log.Printf("Ошибка чтения envelope от %s: %+v", addr, err)
		return
	}
	switch env.Type {
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
		log.Printf("Ошибка декодирования hello от %s: %+v", addr, err)
		return
	}
	peer, ok := addressPeerMap[addr]
	if !ok {
		log.Printf("Unknown hello from: %s", addr)
		return
	}
	memb := peer.Member
	memb.Nick = hello.Nick
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		log.Printf("Ошибка при попытке распарсить адрес=%s: %+v", addr, err)
		return
	}
	memb.PublicAddress = fmt.Sprintf("%s:%s", host, hello.Port)
	log.Printf("Получен hello от member= %+v", memb)
	// Обновляем запись в peersMap: заменяем временный ключ на реальный PeerID.
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
		log.Printf("Ошибка декодирования candidate message от %s: %+v", senderNick, err)
		return
	}
	for _, cand := range candMsg.Candidates {
		if _, ok := nickPeerMap[cand.Nick]; ok {
			continue
		}
		log.Printf("%s прислал кандидата для подключения <Candidate:%+v>", senderNick, cand)

		go connectToPeer(cand.Address)
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
	hello := Hello{
		Nick: nick,
		Port: port,
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
			Nick:    nick,
			Address: peer.PublicAddress,
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
