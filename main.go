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
	PeerID string `json:"peer_id"`
}

// Candidate – информация об узле для рекурсивного подключения.
type Candidate struct {
	PeerID  string `json:"peer_id"`
	Address string `json:"address"`
}

// CandidateMessage – сообщение, содержащее список кандидатов.
type CandidateMessage struct {
	Candidates []Candidate `json:"candidates"`
}

// Peer – информация о подключённом узле.
type Peer struct {
	session *yamux.Session
	Address string
}

// Глобальные переменные и мапы.
var (
	nick         string
	port         string
	parent       string
	localAddress string

	// peersMap хранит активные соединения: ключ – уникальный идентификатор (PeerID).
	peersMap   = make(map[string]*Peer)
	peersMutex sync.Mutex

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
	flag.StringVar(&parent, "parent", "", "Адрес родительского узла (IP:PORT)")
	flag.Parse()

	// Перенаправляем вывод логов так, чтобы они отображались в консоли.
	log.SetOutput(&LogWriter{})

	// Задаём локальный адрес (для тестирования на локальной машине).
	localAddress = "localhost:" + port

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
	log.Fatal(http.ListenAndServe(":"+port, nil))
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
	// До получения hello используем временный идентификатор (r.RemoteAddr).
	tempID := r.RemoteAddr
	peersMutex.Lock()
	peersMap[tempID] = &Peer{session: session, Address: r.RemoteAddr}
	peersMutex.Unlock()

	go acceptStreams(session, tempID)
	// Отправляем кандидатов и hello-сообщение.
	sendCandidates(session)
	sendHello(session)
}

// connectToPeer устанавливает исходящее соединение к заданному адресу.
func connectToPeer(address string) {
	url := "ws://" + address + "/ws"
	log.Printf("[%s] Подключение к %s", nick, address)
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
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
	tempID := address
	peersMutex.Lock()
	peersMap[tempID] = &Peer{session: session, Address: ""}
	peersMutex.Unlock()

	go acceptStreams(session, tempID)
	sendHello(session)
}

// acceptStreams принимает новые потоки из yamux-сессии.
func acceptStreams(session *yamux.Session, tempID string) {
	for {
		stream, err := session.Accept()
		if err != nil {
			log.Printf("Ошибка Accept потока от %s: %v", tempID, err)
			return
		}
		go handleStream(stream, tempID)
	}
}

// handleStream читает Envelope из потока и вызывает соответствующую обработку.
func handleStream(stream net.Conn, tempID string) {
	defer stream.Close()
	decoder := json.NewDecoder(stream)
	var env Envelope
	if err := decoder.Decode(&env); err != nil {
		log.Printf("Ошибка чтения envelope от %s: %v", tempID, err)
		return
	}
	switch env.Type {
	case "hello":
		handleHelloEnvelope(env.Data, tempID)
	case "candidate":
		handleCandidateEnvelope(env.Data, tempID)
	case "chat":
		var msg Message
		if err := json.Unmarshal(env.Data, &msg); err != nil {
			log.Printf("Ошибка декодирования chat-сообщения от %s: %v", tempID, err)
			return
		}
		processChatMessage(msg, tempID)
	default:
		log.Printf("Неизвестный тип envelope от %s: %s", tempID, env.Type)
	}
}

// handleHelloEnvelope обрабатывает handshake-сообщение и обновляет информацию о соединении.
func handleHelloEnvelope(data json.RawMessage, tempID string) {
	var hello Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		log.Printf("Ошибка декодирования hello от %s: %v", tempID, err)
		return
	}
	log.Printf("Получен hello от %s: PeerID=%s, Address=%s", tempID, hello.PeerID, peersMap[tempID].Address)
	// Обновляем запись в peersMap: заменяем временный ключ на реальный PeerID.
	peersMutex.Lock()
	defer peersMutex.Unlock()
	if peer, exists := peersMap[tempID]; exists {
		delete(peersMap, tempID)
		peer.Address = peersMap[tempID].Address
		peersMap[hello.PeerID] = peer
	} else {
		peersMap[hello.PeerID] = &Peer{session: nil, Address: ""}
	}
}

// handleCandidateEnvelope обрабатывает сообщение с кандидатами и инициирует подключение к новым узлам,
// если не достигнут лимит дополнительных соединений и кандидат ещё не пытались подключить.
func handleCandidateEnvelope(data json.RawMessage, tempID string) {
	var candMsg CandidateMessage
	if err := json.Unmarshal(data, &candMsg); err != nil {
		log.Printf("Ошибка декодирования candidate message от %s: %v", tempID, err)
		return
	}
	for _, cand := range candMsg.Candidates {
		// Проверяем, не пытались ли мы уже подключиться к данному кандидату.
		attemptedCandidates.Lock()
		alreadyAttempted := attemptedCandidates.m[cand.PeerID]
		attemptedCandidates.Unlock()
		if alreadyAttempted {
			continue
		}
		peersMutex.Lock()
		_, exists := peersMap[cand.PeerID]
		var currentAdditional int
		// Если у узла есть родитель, считаем, что первое соединение – основное,
		// а все остальные – дополнительные.
		if parent != "" {
			currentAdditional = len(peersMap) - 1
		} else {
			currentAdditional = len(peersMap)
		}
		peersMutex.Unlock()
		if !exists && cand.Address != "" && cand.Address != localAddress && currentAdditional < maxAdditionalConnections {
			log.Printf("Попытка подключения к кандидату %s (%s)", cand.PeerID, cand.Address)
			attemptedCandidates.Lock()
			attemptedCandidates.m[cand.PeerID] = true
			attemptedCandidates.Unlock()
			go connectToPeer(cand.Address)
		}
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
	peersMutex.Lock()
	defer peersMutex.Unlock()
	for peerID, peer := range peersMap {
		if peerID == from {
			continue
		}
		if !contains(msg.HopPath, nick) {
			msg.HopPath = append(msg.HopPath, nick)
		}
		if err := sendEnvelope(peer.session, "chat", msg); err != nil {
			log.Printf("Ошибка ретрансляции через %s: %v", peerID, err)
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
		PeerID: nick,
	}
	if err := sendEnvelope(session, "hello", hello); err != nil {
		log.Printf("Ошибка отправки hello: %v", err)
	}
}

// sendCandidates отправляет список известных кандидатов (не более maxAdditionalConnections) через данную сессию.
func sendCandidates(session *yamux.Session) {
	peersMutex.Lock()
	var candidates []Candidate
	count := 0
	for peerID, peer := range peersMap {
		if peerID == nick {
			continue
		}
		if count >= maxAdditionalConnections {
			break
		}
		if peer.Address != "" {
			candidates = append(candidates, Candidate{
				PeerID:  peerID,
				Address: peer.Address,
			})
			count++
		}
	}
	peersMutex.Unlock()
	candMsg := CandidateMessage{Candidates: candidates}
	if err := sendEnvelope(session, "candidate", candMsg); err != nil {
		log.Printf("Ошибка отправки candidates: %v", err)
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
	peersMutex.Lock()
	defer peersMutex.Unlock()
	for peerID, peer := range peersMap {
		if err := sendEnvelope(peer.session, "chat", msg); err != nil {
			log.Printf("Ошибка отправки сообщения через %s: %v", peerID, err)
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
