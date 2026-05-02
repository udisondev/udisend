# Udisend — Decentralized P2P Messenger — Design Document

> Source of truth для архитектуры. Обновляется по мере реализации. Roadmap и tracking разработки — в `ROADMAP.md`. Правила кодинга — в `CLAUDE.md` и skills под `.claude/skills/`.

> **Major revision:** chat-трафик (текст / файлы / видео-звонки) теперь проходит через WebRTC peer-to-peer. Кастомный сетевой слой (DHT, presence, signaling) существует только для discovery и обмена SDP/ICE. После handshake'а весь user-content идёт напрямую через WebRTC DataChannel и MediaStreamTrack.

---

## 1. Цели проекта и философия

### Главные цели

1. **Отказоустойчивость сети** — нет single point of failure, никаких центральных компонентов, потеря которых ломает систему.
2. **Permissionless-сеть** — любой пользователь может присоединиться без регистрации и сам стать частью инфраструктуры.
3. **Независимость от автора** — после релиза сеть должна жить и развиваться без необходимости в обновлениях от создателя; автор не контролирует и не может контролировать сеть.
4. **Равноправие узлов** — все пользователи равноценны как участники сети; различия только в технических возможностях (публичный IP, аптайм).
5. **End-to-end encrypted chat** — содержимое сообщений, файлов и звонков недоступно никому, кроме отправителя и получателя.

### Принимаемые tradeoff'ы

В обмен на цензуроустойчивость и независимость **сознательно отдаём**:
- Возможность централизованных обновлений и хотфиксов.
- Возможность модерации/банов.
- Простоту операционного управления.
- Гарантированную доставку оффлайн-сообщений (через серверные хранилища).

### Чего НЕ делаем

- **Никакого хранения сообщений в сети** — это потенциальная точка отказа и атаки. Если получатель оффлайн → сообщение лежит в локальном outbox отправителя.
- **Никакого блокчейна, токенов, финансовых стимулов** — это другой проект.
- **Никаких "наших" supernodes** — нет привилегированного класса узлов, контролируемых автором.
- **Никаких групп в MVP** — только 1-на-1.
- **Никакого браузерного клиента в MVP** — desktop Go-only; signaling-протокол совместим с будущим WebSocket-bridge для браузеров.

---

## 2. Базовая архитектура: signaling network + WebRTC chat

### Двухслойная модель

**Слой 1: Signaling network (custom, decentralized)** — кастомная P2P-сеть, единственная задача которой:
- найти другого пользователя по его destination_hash через DHT;
- безопасно обменяться SDP/ICE между двумя пирами;
- предоставлять STUN/TURN серверы для NAT traversal (volunteer узлы).

Этот слой stateless относительно user-content: не видит ни сообщений, ни файлов, ни голоса/видео. Видит только: presence-записи (узел X доступен по такому-то адресу) и signaling-сообщения (зашифрованные blob'ы между двумя пирами).

**Слой 2: WebRTC peer-to-peer (pion)** — после handshake между A и B весь чат-трафик идёт напрямую:
- DataChannel (reliable+ordered SCTP-over-DTLS) — текст, файловые chunks, signaling сообщения уровня приложения (typing, ack, read).
- MediaStreamTrack (SRTP) — audio/video.
- DTLS обеспечивает шифрование и forward secrecy на уровне сессии (свежие ephemeral keys на каждый звонок/линк).

### Многослойная диаграмма

```
┌──────────────────────────────────────────────────────────────────┐
│  Application — chat client                                       │
│  • Message types: text, file_offer, file_chunk, ack, typing,     │
│    call_offer/answer, presence_ping                              │
│  • Local SQLite: история, контакты, outbox, fingerprints         │
│  • TOFU + fingerprint verification (QR / safety numbers)         │
├──────────────────────────────────────────────────────────────────┤
│  WebRTC P2P session (pion/webrtc)                                │
│  • DataChannel — SCTP-over-DTLS, reliable+ordered                │
│  • MediaStreamTrack — SRTP audio/video                           │
│  • DTLS ephemeral keys → per-session forward secrecy             │
├──────────────────────────────────────────────────────────────────┤
│  Signaling layer (custom)                                        │
│  • Find peer via DHT presence по destination_hash                │
│  • Encrypted SDP/ICE exchange (Noise XK over DHT routing)        │
│  • SDP подписан Ed25519, DTLS-fingerprint cross-validated        │
├──────────────────────────────────────────────────────────────────┤
│  Discovery / DHT / Volunteer NAT services                        │
│  • Kademlia + S/Kademlia hardening                               │
│  • Presence records: signed, ephemeral, TTL 60-120s              │
│  • Capability roles: PublicIP, CanRelay, CanSTUN, CanTURN        │
│  • Bootstrap: hardcoded list + cache + DNS-seeds                 │
├──────────────────────────────────────────────────────────────────┤
│  Identity                                                        │
│  • Ed25519 (sign) + X25519 (KEX)                                 │
│  • destination_hash = first 16 bytes of SHA-256(ed_pub‖x_pub)    │
└──────────────────────────────────────────────────────────────────┘
```

### Принцип "end-to-end"

Signaling-сеть — это тупая routing fabric (аналогично IP). Узлы только маршрутизируют сигнальные пакеты, не понимая их семантики. Весь user-content end-to-end зашифрован и идёт по прямому WebRTC-каналу.

### Разделение ответственности

**Signaling network (library):**
- DHT для discovery (поиск пиров по destination_hash).
- Транспорт UDP+DTLS для DHT-сообщений и signaling.
- Presence records (signed, ephemeral).
- STUN/TURN сервисы (volunteer узлы).
- Маршрутизация signaling blob'ов между пирами.

**WebRTC layer (library, обёртка над pion):**
- PeerConnection lifecycle.
- SDP offer/answer создание + парсинг.
- ICE candidate gathering и обмен.
- DataChannel / MediaTrack API для приложения.

**Application layer (client):**
- Идентичность пользователя, контакты, fingerprint verification.
- Application protocol поверх DataChannel: типы сообщений, framing, ACK.
- File chunking логика.
- Outbox для оффлайн-получателей.
- История сообщений (SQLite).
- UI, уведомления.

### Целевой API

```go
// Signaling — нашли пира и установили зашифрованный signaling-канал
type Signaling interface {
    Connect(ctx context.Context, peer identity.Hash) (Channel, error)
    Listen(handler func(peer identity.Hash, ch Channel))
}

// WebRTC session поверх Signaling
type Session interface {
    OpenDataChannel(label string) (DataChannel, error)
    AddVideoTrack(track Track) error
    AddAudioTrack(track Track) error
    Close() error
}

// Высокоуровневый API клиента
type Messenger interface {
    SendMessage(ctx context.Context, peer identity.Hash, msg Message) error
    StartCall(ctx context.Context, peer identity.Hash) (Call, error)
    OnMessage(handler func(from identity.Hash, msg Message))
    OnIncomingCall(handler func(from identity.Hash, call Call))
}
```

---

## 3. Идентичность и криптография

### Идентичность пользователя

- Пара ключей: **Ed25519** (подписи) + **X25519** (key exchange).
- Часто выводятся из одного seed.
- **Destination hash** = первые 16 байт SHA-256 от конкатенации публичных ключей `ed_pub ‖ x_pub`.
- Адрес пользователя = его destination hash. Никаких имён, регистраций, DNS.

### Криптографический набор (hardcoded)

Без negotiation. Все клиенты используют одно и то же:

- **X25519** — ECDH (signaling handshake).
- **Ed25519** — подписи (presence, SDP).
- **ChaCha20-Poly1305** — AEAD (signaling канал).
- **BLAKE2b** — хеши.
- **HKDF** — key derivation.

WebRTC слой использует свой набор (DTLS 1.3 + AES-GCM или ChaCha20-Poly1305 + curve25519/P-256, всё внутри pion). Мы не контролируем этот выбор — он фиксирован стандартом WebRTC.

### Crypto agility через identifiers

В формате полей крипто-материалов есть `algorithm_id`. Сейчас всегда `0x01`. Когда понадобится — выпускается клиент с поддержкой `0x02` (например, post-quantum). Старые клиенты используют `0x01`, новые между собой могут `0x02`.

### Двухуровневая криптография

1. **Signaling layer** — Noise XK end-to-end между двумя пирами по их identity keys. Signaling сообщения проходят через relay-узлы — без шифрования relay'и видели бы SDP.

2. **WebRTC layer** — DTLS 1.3 между клиентами. Сертификат self-signed, его fingerprint объявляется в SDP. Forward secrecy получается от ephemeral DH-ключей DTLS на каждой сессии.

### SDP↔Identity binding (защита от MITM на signaling)

Это критический протокольный момент:

1. Клиент A генерирует SDP-offer (содержит DTLS-fingerprint его одноразового сертификата).
2. A подписывает (SDP + ICE candidates) Ed25519-ключом своей идентичности → `signed_offer`.
3. A шлёт `signed_offer` через signaling-канал к B (зашифровано Noise XK).
4. B расшифровывает, проверяет Ed25519-подпись против известного pubkey пира A (TOFU при первом контакте).
5. B принимает SDP, генерирует свой signed answer тем же путём.
6. При установке WebRTC-сессии B проверяет, что DTLS-fingerprint, который он видит в реальном handshake, совпадает с fingerprint из подписанного SDP.
7. Если MITM подменил SDP по пути — fingerprint не совпадёт, сессия рвётся.

Без этого binding signaling-relay смог бы подменить SDP и провести MITM на WebRTC.

### TOFU + fingerprint verification

При первом контакте с пиром клиент сохраняет его Ed25519 pubkey как идентичность. Опциональная out-of-band проверка через QR-код или числовую строку в стиле Signal safety numbers — для верификации, что pubkey действительно принадлежит правильному человеку.

---

## 4. Discovery: гибрид DHT + announce

### Почему DHT, а не чисто Reticulum-style announce

Reticulum-подход (gossip-распространение announce'ов по всей сети) **не масштабируется** — каждый узел должен знать всех. Работает до ~10K пользователей, дальше деградирует квадратично.

Поэтому: **Kademlia DHT** как фундамент, с announce-оптимизациями для активных контактов.

### Архитектура discovery

**Kademlia DHT** хранит только эфемерные presence-записи:

```
key   = destination_hash (16 байт)
value = {ip:port, capabilities, timestamp, signature}
TTL   = 60-120 секунд
```

- Запись подписана приватным ключом владельца — никто не может опубликовать фейковый адрес.
- DHT-узлы хранят blob, не могут подделать.
- Узел оффлайн → запись истекает естественно.

**Параметры Kademlia:**
- K-bucket size: 20.
- ID space: 128 бит (destination hash = первые 16 байт SHA-256 от `ed_pub‖x_pub`, см. §3).
- Replication factor: K (presence-запись хранится на K ближайших узлах).

### Защита от Sybil — S/Kademlia

Базовая Kademlia уязвима для Sybil-атак (атакующий создаёт много фейковых узлов вокруг жертвы). Используем **S/Kademlia** с:
- Proof-of-work на генерацию nodeID (опциональный, лёгкий).
- Disjoint paths при lookup (несколько независимых путей).
- Sibling lists для реплики критичных записей.

### Announce-оптимизация для активных контактов

Когда два клиента имеют активную WebRTC-сессию — они шлют presence-обновления друг другу через DataChannel напрямую, в обход DHT. Это:
- Резко снижает DHT-нагрузку для типичных пар "часто общающихся".
- Уменьшает latency на повторное соединение.
- Работает как "теплый кэш" поверх DHT.

### Path discovery в signaling

Маршрутизация signaling-сообщений **hop-by-hop**:
- Клиент шлёт signaling blob с `recipient_id`, не зная пути.
- Каждый промежуточный узел смотрит в свою таблицу, шлёт следующему хопу.
- Если destination не в таблице → path request на соседей через DHT.
- Если ничего не найдено → пакет дропается, клиент получает таймаут.

(Этот hop-by-hop routing **только** для signaling. Чат-трафик идёт напрямую через WebRTC peer-to-peer.)

---

## 5. Capability-based роли узлов

### Никаких "supernode" vs "regular"

Все узлы равны. Роли определяются динамически по capabilities:

```go
type NodeCapabilities struct {
    PublicIP      bool   // может принимать входящие
    CanRelay      bool   // готов маршрутизировать signaling-blob'ы
    CanBootstrap  bool   // принимает bootstrap-запросы
    CanSTUN       bool   // обслуживает STUN-запросы
    CanTURN       bool   // обслуживает TURN-relay для WebRTC
    UptimeHint    uint32 // секунд аптайма
    MaxRelaySlots uint16 // сколько готов обслуживать
}
```

Capabilities публикуются в presence-записи. Клиенты используют эту информацию при выборе ICE servers и signaling-relay'ев.

### Естественное разделение нагрузки

В реальной сети ожидаем распределение примерно:
- **5-15%** узлов с публичным IP — VPS, домашние серверы, NAS.
- **70-80%** за обычным NAT — пробиваются через UDP hole punching.
- **10-20%** за CGNAT/симметричным NAT — обязательно нужен TURN-relay.

Узлы с публичным IP **автоматически** начинают обслуживать STUN/TURN/bootstrap для других. Никакой явной активации, никакого whitelist.

### Стимул быть полезным

Финансовых стимулов нет. Стимулы дизайн-уровня:
- **Запуск network-узла = участие в сети** — даже если ты сам не пишешь сообщения, твой узел помогает.
- **Дефолт — помогать**, опт-аут возможен.
- **Карма/репутация (опционально, post-MVP)** — узлы запоминают полезных соседей и приоритизируют их.

---

## 6. NAT traversal через WebRTC ICE

WebRTC из коробки решает NAT traversal через ICE (Interactive Connectivity Establishment). Наша задача — **предоставить ICE servers**.

### Стандартный ICE flow

1. Каждый пир собирает кандидатов:
   - **Host** — локальный IP:port.
   - **Server reflexive (srflx)** — внешний IP:port через STUN.
   - **Relay** — IP:port на TURN-сервере, через который пойдёт трафик при невозможности прямого соединения.
2. Пиры обмениваются кандидатами через signaling.
3. ICE пробует все пары (locally → srflx → relay) параллельно, выбирает первую работающую.
4. Если прямой путь работает — медиа/данные идут peer-to-peer.
5. Если нет — через TURN-relay (zashифровано DTLS, relay видит только зашифрованные байты).

### Volunteer ICE servers

Клиент **не имеет жёстко прописанных** STUN/TURN адресов. Вместо этого:
1. Перед инициированием звонка/линка клиент делает DHT lookup узлов с capability `CanSTUN`/`CanTURN`.
2. Выбирает 3-5 узлов по latency/uptime.
3. Передаёт их адреса в `webrtc.Configuration.ICEServers`.

Узлы с публичным IP запускают встроенные STUN/TURN серверы (`pion/stun`, `pion/turn`) и рекламируют capabilities в presence.

### Почему отдаём NAT traversal стандартному ICE

- ICE проверен годами, реализован в браузерах.
- pion/webrtc делает всю работу.
- Не нужно изобретать кастомный hole punching.

### Mobile clients — second-class

Реалистично: mobile-клиенты не могут полноценно участвовать как network-узлы из-за batterly/networking ограничений. Подходы (откладываются):
- **Опрос при пробуждении** — клиент при открытии тянет presence + outbox.
- **Push-уведомления через relay** — требует APNS/FCM, что само по себе централизация (tradeoff).
- **Принять, что mobile работает только в открытом приложении** — позиция Briar.

В MVP mobile нет.

---

## 7. Bootstrap и защита от смерти сети

### Принцип "после релиза автор может исчезнуть"

Сеть должна продолжать жить без обновлений и без участия автора.

### Bootstrap-список

- В клиенте **хардкодится 20-30 bootstrap-адресов** от разных людей в разных юрисдикциях (не только автора).
- Клиент после первого подключения **сохраняет адреса 50-100 узлов**, с которыми контактировал.
- Следующий запуск — идёт в кэш, не нуждается в bootstrap-списке.
- **Любой пользователь с публичным IP** может объявить себя bootstrap'ом.

### Отказоустойчивость bootstrap'а

- **DNS-seeds** (как в Bitcoin) — `seed1.example.org`, `seed2.example.org`, резолвящиеся в актуальные узлы.
- **Резервные хардкоднутые IP** на случай DNS-цензуры.
- **Сеть самовоспроизводится** — каждый новый клиент через час работы знает сотни узлов.

### Версионирование протокола

Каждый сигнальный пакет содержит:
- `version` — старые клиенты могут корректно проигнорировать пакеты новой версии.
- `type` — добавление новых типов сообщений без поломки совместимости.
- `extensions` (TLV) — опциональные поля в конце; неизвестные пропускаются.
- **Feature flags в handshake** — клиенты обмениваются capabilities, используют пересечение.

### Открытая спецификация и пермиссивная лицензия

- **Спецификация протокола в текстовом виде**, отдельно от кода.
- **MIT/Apache/BSD лицензия** — не GPL, не ограничивать форки.
- **Цель — несколько независимых реализаций** (как Bitcoin Core + btcd, Tor + Arti).
- Привлекать контрибьюторов, давать commit access.

---

## 8. Threat model и атаки

### Уровень сети

| Атака | Защита |
|---|---|
| **Eclipse attack** на DHT | Подключение к разнообразным пирам (по подсетям, ASN, географии); хардкоднутые bootstrap'ы от разных операторов |
| **Sybil на presence** | Подпись записи владельцем; лимит на записи с одного IP; PoW на регистрацию (опционально) |
| **MITM на signaling** | SDP+ICE подписаны Ed25519; DTLS-fingerprint cross-validated при WebRTC handshake |
| **MITM на первом контакте** | TOFU + проверка fingerprint вне канала (QR-код, safety numbers) |
| **DoS на узел** | Rate limiting на DHT-уровне; multi-hop routing для размазывания |
| **Подмена presence-записи** | Ed25519-подпись, валидация на всех узлах |
| **Traffic analysis (signaling relay видит metadata)** | Onion routing для signaling — на будущее |
| **TURN-relay перехватывает медиа** | TURN видит только зашифрованные DTLS-байты; невозможен decrypt |
| **Compromised volunteer node** | Один узел не может MITM (signaling зашифрован Noise XK end-to-end); максимум — refuse to relay |

### Уровень WebRTC

- **Forward secrecy** — DTLS ephemeral keys на каждой сессии.
- **Identity binding** — DTLS-fingerprint в SDP подписан Ed25519, проверяется при handshake.
- **Replay attack** — DTLS handshake включает freshness; SDP подписан с timestamp.
- **DataChannel / MediaTrack confidentiality** — шифруются DTLS/SRTP, контент недоступен relay'ям.

### Социальные риски

- **Использование сети для CSAM/террора** — позиция "tool, не модератор". Технически невозможно видеть содержимое (E2E).
- **Зловредные форки клиента** — reproducible builds, подписанные релизы, transparent history.
- **Государственная блокировка bootstrap'ов** — pluggable transports (как Tor), bridge nodes. Откладываем.

---

## 9. Сообщения и user content

### Транспорт сообщений и файлов: WebRTC DataChannel

После установки сессии все сообщения и файлы идут через DataChannel:
- Reliable + ordered (SCTP режим по умолчанию).
- DTLS handles confidentiality + integrity.
- DataChannel умеет fragmentation сам — мы шлём cборку application-level сообщения, SCTP режет на network frames.

### Аудио и видео: MediaStreamTrack

- Стандартный WebRTC: захват с микрофона/камеры, кодирование (Opus / VP8 / VP9 / AV1 — выбирает pion).
- SRTP для шифрования.
- ICE автоматически выберет direct path или TURN-relay.

### Application protocol поверх DataChannel

```
ApplicationMessage {
  req_id          : UUID         // дедупликация и matching ACK
  message_type    : enum         // text / file_offer / file_chunk /
                                 // ack / typing / read / call_signal /
                                 // presence_ping
  timestamp       : int64
  body            : bytes        // payload зависит от message_type
}
```

### Доставка и ACK

- **Базовый протокол** — fire and forget с end-to-end ACK на уровне приложения.
- DataChannel reliable+ordered гарантирует доставку, пока сессия жива.
- Если сессия рвётся — сообщения, не получившие ACK, считаются неотправленными → возвращаются в outbox.

### Outbox для оффлайн-получателей

- **Сеть НЕ отвечает "получатель оффлайн"** — никакого хранения сообщений в сети.
- Если попытка установить WebRTC-сессию провалилась (получатель не публикует presence или signaling timeout) → сообщение в локальный outbox.
- Outbox **периодически проверяет** появление получателя в presence (DHT polling с экспоненциальным backoff).
- При появлении — устанавливается сессия, outbox сливается.
- Только сообщения и file-offers хранятся в outbox (не payload файлов).

### Файлы — chunking на application level

- Большой файл: file_offer с метаданными (имя, размер, хеш) + получатель ACK'ает.
- Затем серия file_chunk сообщений (фиксированный размер, например 64 KB).
- Per-chunk ACK для resume support при разрыве сессии.
- DataChannel умеет flow control сам, но мы дополнительно следим за окном.

### Звонки

- **Установка** — call_offer через DataChannel; получатель отвечает call_answer.
- После accept — добавляются audio/video MediaStreamTrack'и в существующую PeerConnection.
- При отказе/завершении — call_signal с reason; сессия может остаться открытой для текста.

### Multi-device

Откладывается. Для MVP: один pubkey = одно устройство.

В будущем:
- Каждое устройство имеет свой pubkey.
- Между устройствами одного юзера отдельная sync-логика (как в Matrix).
- Presence содержит список активных устройств одного юзера.

### Группы

Сложная отдельная тема. Для MVP **не делаем**, только 1-на-1.

Будущие подходы:
- **Mesh** для маленьких групп (3-6 человек): N(N-1)/2 connections.
- **SFU (Selective Forwarding Unit)** для больших — узел-relay forwarding'ит media tracks. Кандидат на роль — узлы с capability `CanSFU`.
- **Crypto:** MLS (Messaging Layer Security) для групповых ключей.

---

## 10. Технологический стек

### Язык и основные библиотеки

- **Go 1.26** — основной язык (per `CLAUDE.md`).
- **`github.com/pion/webrtc/v4`** — WebRTC stack (PeerConnection, DataChannel, media tracks).
- **`github.com/pion/stun/v3`** — встроенный STUN-сервер для volunteer узлов.
- **`github.com/pion/turn/v4`** — встроенный TURN-сервер для volunteer узлов.
- **`github.com/flynn/noise`** — Noise Protocol Framework для signaling-канала.
- **`crypto/ed25519`, `golang.org/x/crypto/curve25519`** — стандартная библиотека Go.
- **`golang.org/x/crypto/chacha20poly1305`** — AEAD.
- **`golang.org/x/crypto/blake2b`** — хеши.
- **`golang.org/x/crypto/hkdf`** — KDF.

### Хранение (только локальное, на клиенте)

- **SQLite** через `modernc.org/sqlite` (pure Go, без CGO) — история сообщений, контакты, outbox, fingerprints.
- **Никакого распределённого хранилища.**

### Тестирование

- `github.com/google/go-cmp` — `cmp.Diff` для assertion'ов.
- `github.com/testcontainers/testcontainers-go` — multi-node integration tests.
- `testing/synctest` (stdlib, 1.25+) — детерминистические concurrency-тесты.
- `testify/suite` — только для e2e (см. `go-testing` skill).

### Транспорт-абстракция

В коде транспорт DHT/signaling — это **interface**, не захардкоженный UDP:

```go
type Transport interface {
    Send(ctx context.Context, to NetAddress, packet []byte) error
    Receive() <-chan IncomingPacket
}
```

UDP — одна из реализаций. Это даёт гибкость для добавления Bluetooth/Tor/etc позже.

WebRTC использует свой UDP внутри pion — мы не управляем им напрямую.

---

## 11. Roadmap

См. `ROADMAP.md` — там фазированный план с трекингом, acceptance criteria и checkboxes по конкретным task'ам каждой фазы. Этот document описывает **архитектуру**, ROADMAP — **план реализации**.

Краткая сводка:

| Фаза | Цель | Срок |
|---|---|---|
| 1 | Identity foundation (`pkg/identity`, `pkg/crypto`) | ~1 нед |
| 2 | Local Kademlia DHT (`pkg/dht`, `pkg/transport`) | ~3 нед |
| 3 | Presence + S/Kademlia (`pkg/presence`) | ~1.5 нед |
| 4 | Signaling channel (`pkg/noise`, `pkg/signaling`) | ~2 нед |
| 5 | STUN/TURN volunteers (`pkg/stun`, `pkg/turn`) | ~1 нед |
| 6 | WebRTC session (`pkg/webrtc`) | ~2 нед |
| 7 | Chat application (`internal/chat`, `internal/storage`, `internal/messenger`, `internal/network`, `internal/httpui`) — v2: drop fyne, browser owns WebRTC | ~3-4 нед |
| 8 | Hardening & MVP release | ~2 нед |

---

## 12. Архитектурные принципы (cheat sheet)

1. **Signaling network is dumb, clients are smart** — сетевой слой не знает семантики чата.
2. **No state in network** — сеть stateless кроме DHT routing table; никаких хранилищ сообщений.
3. **All nodes are equal as users** — capabilities определяют технические роли, не привилегии.
4. **Identity = key, address = hash(key)** — никаких имён, регистраций, DNS.
5. **Hardcoded crypto suite (для signaling)** — никакого algorithm negotiation, но extension points для будущего. WebRTC уровень использует свой стандартный stack.
6. **Versioned protocol from day 1** — TLV extensions, feature flags.
7. **Open spec, permissive license, multiple implementations** — проект переживает автора.
8. **Direct peer-to-peer chat** — после signaling handshake'а сетевая инфра не видит content.
9. **Default to helping** — клиент = участник сети по умолчанию.
10. **Conservative complexity** — чем проще протокол, тем меньше его придётся менять.

---

## 13. Открытые вопросы (на будущее)

Решённые в ходе реализации:
- ✅ Wire format для signaling — кастомный uvarint-prefixed binary (`pkg/wire`), решение Phase 4 / decisions log 2026-05-01.
- ✅ UI-библиотека — изначально fyne, в v2 (2026-05-02) переделано на browser UI поверх HTTP+SSE.
- ✅ SQLite driver — `modernc.org/sqlite` (pure Go).

Открыты:
- Конкретные параметры PoW в S/Kademlia — Phase 3 deferred, hardening pass.
- Стратегия выбора disjoint paths — Phase 3 deferred.
- Push-уведомления для mobile (если делать).
- Onion routing для signaling traffic-analysis resistance — post-MVP.
- Механизм синхронизации между устройствами одного пользователя — post-MVP.

---

## Приложение: что взято из Reticulum

- Identity = truncated hash of pubkey (16 байт).
- Hop-by-hop routing для signaling вместо source routing.
- Hardcoded crypto suite.
- Transport-agnostic архитектура (interface).
- Path requests как fallback discovery.

## Приложение: что НЕ взято из Reticulum

- Чисто-announce presence (не масштабируется) — заменяем на DHT.
- AES-128-CBC — устаревший выбор; используем ChaCha20-Poly1305.
- Иерархическое именование destinations (избыточно).
- Кастомный ratchet для chat-трафика — у нас WebRTC DTLS.

## Приложение: референсы для изучения

- **[Kademlia paper](https://pdos.csail.mit.edu/~petar/papers/maymounkov-kademlia-lncs.pdf)** — Maymounkov & Mazières.
- **[S/Kademlia paper](https://citeseerx.ist.psu.edu/document?repid=rep1&type=pdf&doi=10.1.1.68.4986)** — Sybil-resistant Kademlia.
- **[Noise Protocol Framework](https://noiseprotocol.org/)** — особенно XK pattern.
- **[pion/webrtc](https://github.com/pion/webrtc)** — Go WebRTC implementation.
- **[WebRTC for the Curious](https://webrtcforthecurious.com/)** — практическое руководство (от создателей pion).
- **[RFC 8825 — WebRTC overview](https://datatracker.ietf.org/doc/html/rfc8825)**.
- **[Tox](https://tox.chat/)** — близкий по духу проект.
- **[Briar](https://briarproject.org/)** — другой близкий проект, mobile-first.
- **[End-to-End Arguments in System Design](http://web.mit.edu/Saltzer/www/publications/endtoend/endtoend.pdf)** — Saltzer, Reed, Clark 1984.
