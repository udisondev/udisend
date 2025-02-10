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

var stunServers = []string{
	"23.21.150.121:3478",
	"iphone-stun.strato-iphone.de:3478",
	"numb.viagenie.ca:3478",
	"s1.taraba.net:3478",
	"s2.taraba.net:3478",
	"stun.12connect.com:3478",
	"stun.12voip.com:3478",
	"stun.1und1.de:3478",
	"stun.2talk.co.nz:3478",
	"stun.2talk.com:3478",
	"stun.3clogic.com:3478",
	"stun.3cx.com:3478",
	"stun.a-mm.tv:3478",
	"stun.aa.net.uk:3478",
	"stun.acrobits.cz:3478",
	"stun.actionvoip.com:3478",
	"stun.advfn.com:3478",
	"stun.aeta-audio.com:3478",
	"stun.aeta.com:3478",
	"stun.alltel.com.au:3478",
	"stun.altar.com.pl:3478",
	"stun.annatel.net:3478",
	"stun.antisip.com:3478",
	"stun.arbuz.ru:3478",
	"stun.avigora.com:3478",
	"stun.avigora.fr:3478",
	"stun.awa-shima.com:3478",
	"stun.awt.be:3478",
	"stun.b2b2c.ca:3478",
	"stun.bahnhof.net:3478",
	"stun.barracuda.com:3478",
	"stun.bluesip.net:3478",
	"stun.bmwgs.cz:3478",
	"stun.botonakis.com:3478",
	"stun.budgetphone.nl:3478",
	"stun.budgetsip.com:3478",
	"stun.cablenet-as.net:3478",
	"stun.callromania.ro:3478",
	"stun.callwithus.com:3478",
	"stun.cbsys.net:3478",
	"stun.chathelp.ru:3478",
	"stun.cheapvoip.com:3478",
	"stun.ciktel.com:3478",
	"stun.cloopen.com:3478",
	"stun.colouredlines.com.au:3478",
	"stun.comfi.com:3478",
	"stun.commpeak.com:3478",
	"stun.comtube.com:3478",
	"stun.comtube.ru:3478",
	"stun.cope.es:3478",
	"stun.counterpath.com:3478",
	"stun.counterpath.net:3478",
	"stun.cryptonit.net:3478",
	"stun.darioflaccovio.it:3478",
	"stun.datamanagement.it:3478",
	"stun.dcalling.de:3478",
	"stun.decanet.fr:3478",
	"stun.demos.ru:3478",
	"stun.develz.org:3478",
	"stun.dingaling.ca:3478",
	"stun.doublerobotics.com:3478",
	"stun.drogon.net:3478",
	"stun.duocom.es:3478",
	"stun.dus.net:3478",
	"stun.e-fon.ch:3478",
	"stun.easybell.de:3478",
	"stun.easycall.pl:3478",
	"stun.easyvoip.com:3478",
	"stun.efficace-factory.com:3478",
	"stun.einsundeins.com:3478",
	"stun.einsundeins.de:3478",
	"stun.ekiga.net:3478",
	"stun.epygi.com:3478",
	"stun.etoilediese.fr:3478",
	"stun.eyeball.com:3478",
	"stun.faktortel.com.au:3478",
	"stun.freecall.com:3478",
	"stun.freeswitch.org:3478",
	"stun.freevoipdeal.com:3478",
	"stun.fuzemeeting.com:3478",
	"stun.gmx.de:3478",
	"stun.gmx.net:3478",
	"stun.gradwell.com:3478",
	"stun.halonet.pl:3478",
	"stun.hellonanu.com:3478",
	"stun.hoiio.com:3478",
	"stun.hosteurope.de:3478",
	"stun.ideasip.com:3478",
	"stun.imesh.com:3478",
	"stun.infra.net:3478",
	"stun.internetcalls.com:3478",
	"stun.intervoip.com:3478",
	"stun.ipcomms.net:3478",
	"stun.ipfire.org:3478",
	"stun.ippi.fr:3478",
	"stun.ipshka.com:3478",
	"stun.iptel.org:3478",
	"stun.irian.at:3478",
	"stun.it1.hr:3478",
	"stun.ivao.aero:3478",
	"stun.jappix.com:3478",
	"stun.jumblo.com:3478",
	"stun.justvoip.com:3478",
	"stun.kanet.ru:3478",
	"stun.kiwilink.co.nz:3478",
	"stun.kundenserver.de:3478",
	"stun.l.google.com:19302",
	"stun.linea7.net:3478",
	"stun.linphone.org:3478",
	"stun.liveo.fr:3478",
	"stun.lowratevoip.com:3478",
	"stun.lugosoft.com:3478",
	"stun.lundimatin.fr:3478",
	"stun.magnet.ie:3478",
	"stun.manle.com:3478",
	"stun.mgn.ru:3478",
	"stun.mit.de:3478",
	"stun.mitake.com.tw:3478",
	"stun.miwifi.com:3478",
	"stun.modulus.gr:3478",
	"stun.mozcom.com:3478",
	"stun.myvoiptraffic.com:3478",
	"stun.mywatson.it:3478",
	"stun.nas.net:3478",
	"stun.neotel.co.za:3478",
	"stun.netappel.com:3478",
	"stun.netappel.fr:3478",
	"stun.netgsm.com.tr:3478",
	"stun.nfon.net:3478",
	"stun.noblogs.org:3478",
	"stun.noc.ams-ix.net:3478",
	"stun.node4.co.uk:3478",
	"stun.nonoh.net:3478",
	"stun.nottingham.ac.uk:3478",
	"stun.nova.is:3478",
	"stun.nventure.com:3478",
	"stun.on.net.mk:3478",
	"stun.ooma.com:3478",
	"stun.ooonet.ru:3478",
	"stun.oriontelekom.rs:3478",
	"stun.outland-net.de:3478",
	"stun.ozekiphone.com:3478",
	"stun.patlive.com:3478",
	"stun.personal-voip.de:3478",
	"stun.petcube.com:3478",
	"stun.phone.com:3478",
	"stun.phoneserve.com:3478",
	"stun.pjsip.org:3478",
	"stun.poivy.com:3478",
	"stun.powerpbx.org:3478",
	"stun.powervoip.com:3478",
	"stun.ppdi.com:3478",
	"stun.prizee.com:3478",
	"stun.qq.com:3478",
	"stun.qvod.com:3478",
	"stun.rackco.com:3478",
	"stun.rapidnet.de:3478",
	"stun.rb-net.com:3478",
	"stun.refint.net:3478",
	"stun.remote-learner.net:3478",
	"stun.rixtelecom.se:3478",
	"stun.rockenstein.de:3478",
	"stun.rolmail.net:3478",
	"stun.rounds.com:3478",
	"stun.rynga.com:3478",
	"stun.samsungsmartcam.com:3478",
	"stun.schlund.de:3478",
	"stun.services.mozilla.com:3478",
	"stun.sigmavoip.com:3478",
	"stun.sip.us:3478",
	"stun.sipdiscount.com:3478",
	"stun.siplogin.de:3478",
	"stun.sipnet.net:3478",
	"stun.sipnet.ru:3478",
	"stun.siportal.it:3478",
	"stun.sippeer.dk:3478",
	"stun.siptraffic.com:3478",
	"stun.skylink.ru:3478",
	"stun.sma.de:3478",
	"stun.smartvoip.com:3478",
	"stun.smsdiscount.com:3478",
	"stun.snafu.de:3478",
	"stun.softjoys.com:3478",
	"stun.solcon.nl:3478",
	"stun.solnet.ch:3478",
	"stun.sonetel.com:3478",
	"stun.sonetel.net:3478",
	"stun.sovtest.ru:3478",
	"stun.speedy.com.ar:3478",
	"stun.spokn.com:3478",
	"stun.srce.hr:3478",
	"stun.ssl7.net:3478",
	"stun.stunprotocol.org:3478",
	"stun.symform.com:3478",
	"stun.symplicity.com:3478",
	"stun.sysadminman.net:3478",
	"stun.t-online.de:3478",
	"stun.tagan.ru:3478",
	"stun.tatneft.ru:3478",
	"stun.teachercreated.com:3478",
	"stun.tel.lu:3478",
	"stun.telbo.com:3478",
	"stun.telefacil.com:3478",
	"stun.tis-dialog.ru:3478",
	"stun.tng.de:3478",
	"stun.twt.it:3478",
	"stun.u-blox.com:3478",
	"stun.ucallweconn.net:3478",
	"stun.ucsb.edu:3478",
	"stun.ucw.cz:3478",
	"stun.uls.co.za:3478",
	"stun.unseen.is:3478",
	"stun.usfamily.net:3478",
	"stun.veoh.com:3478",
	"stun.vidyo.com:3478",
	"stun.vipgroup.net:3478",
	"stun.virtual-call.com:3478",
	"stun.viva.gr:3478",
	"stun.vivox.com:3478",
	"stun.vline.com:3478",
	"stun.vo.lu:3478",
	"stun.vodafone.ro:3478",
	"stun.voicetrading.com:3478",
	"stun.voip.aebc.com:3478",
	"stun.voip.blackberry.com:3478",
	"stun.voip.eutelia.it:3478",
	"stun.voiparound.com:3478",
	"stun.voipblast.com:3478",
	"stun.voipbuster.com:3478",
	"stun.voipbusterpro.com:3478",
	"stun.voipcheap.co.uk:3478",
	"stun.voipcheap.com:3478",
	"stun.voipfibre.com:3478",
	"stun.voipgain.com:3478",
	"stun.voipgate.com:3478",
	"stun.voipinfocenter.com:3478",
	"stun.voipplanet.nl:3478",
	"stun.voippro.com:3478",
	"stun.voipraider.com:3478",
	"stun.voipstunt.com:3478",
	"stun.voipwise.com:3478",
	"stun.voipzoom.com:3478",
	"stun.vopium.com:3478",
	"stun.voxgratia.org:3478",
	"stun.voxox.com:3478",
	"stun.voys.nl:3478",
	"stun.voztele.com:3478",
	"stun.vyke.com:3478",
	"stun.webcalldirect.com:3478",
	"stun.whoi.edu:3478",
	"stun.wifirst.net:3478",
	"stun.wwdl.net:3478",
	"stun.xs4all.nl:3478",
	"stun.xtratelecom.es:3478",
	"stun.yesss.at:3478",
	"stun.zadarma.com:3478",
	"stun.zadv.com:3478",
	"stun.zoiper.com:3478",
	"stun1.faktortel.com.au:3478",
	"stun1.l.google.com:19302",
	"stun1.voiceeclipse.net:3478",
	"stun2.l.google.com:19302",
	"stun3.l.google.com:19302",
	"stun4.l.google.com:19302",
	"stunserver.org:3478",
	"stun.sipnet.net:3478",
	"stun.sipnet.ru:3478",
	"stun.stunprotocol.org:3478",
	"124.64.206.224:8800",
	"stun.nextcloud.com:443",
	"relay.webwormhole.io",
	"stun.flashdance.cx:3478",
}

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
	Nick            string   `json:"peer_id"`
	Port            string   `json:"port"`
	PublicAddresses []string `json:"public_addresses"`
}

// Candidate – информация об узле для рекурсивного подключения.
type Candidate struct {
	Nick      string   `json:"peer_id"`
	Addresses []string `json:"address"`
}

type Member struct {
	Nick            string
	Address         string
	PublicAddresses []string
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
	addressPeerMap[r.RemoteAddr] = &Peer{session: session, Member: &Member{Nick: "", Address: r.RemoteAddr}}
	addrPeerMapMutex.Unlock()

	go acceptStreams(session, r.RemoteAddr)
	// Отправляем кандидатов и hello-сообщение.
	sendCandidates(session, r.RemoteAddr)
	sendHello(session)
}

// connectToPeer устанавливает исходящее соединение к заданному адресу.
func connectToPeer(address string) bool {
	uri := "ws://" + address + "/ws"
	log.Printf("[%s] Подключение к %s", nick, address)
	ws, _, err := websocket.DefaultDialer.Dial(uri, nil)
	if err != nil {
		log.Println("Ошибка Dial:", err)
		return false
	}
	conn := websocketToConn(ws)
	session, err := yamux.Client(conn, nil)
	if err != nil {
		log.Println("Ошибка создания yamux клиента:", err)
		return false
	}
	// Сохраняем соединение временно по адресу до handshake.
	addrPeerMapMutex.Lock()
	addressPeerMap[address] = &Peer{session: session, Member: &Member{Address: address}}
	addrPeerMapMutex.Unlock()
	log.Printf("Добавлен peer addr: %s", address)

	go acceptStreams(session, address)
	sendHello(session)
	return true
}

// acceptStreams принимает новые потоки из yamux-сессии.
func acceptStreams(session *yamux.Session, addr string) {
	for {
		stream, err := session.Accept()
		if err != nil {
			log.Printf("Ошибка Accept потока от %s: %+v", addr, err)
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
		log.Printf("Ошибка чтения envelope от %s: %+v", addr, err)
		return
	}
	switch env.Type {
	case "ping":
		encoder := json.NewEncoder(stream)
		err := encoder.Encode(Envelope{
			Type: "pong",
		})
		if err != nil {
			log.Printf("Ошибка отправки pong")
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
	memb.PublicAddresses = hello.PublicAddresses
	log.Printf("Получен hello от member= %+v", memb)
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

		for _, addr := range cand.Addresses {
			if connectToPeer(addr) {
				log.Printf("Соединение с nick=%s установлено по addr=%s", cand.Nick, addr)
				break
			}
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
