# udisend — Claude Code instructions

Этот файл — durable инструкции для Claude Code в этом проекте. Полный архитектурный контекст — в `p2p-messenger-design.md`.

---

## Что это

Децентрализованный P2P-мессенджер на Go. См. `p2p-messenger-design.md` для архитектуры, философии, threat model и roadmap.

---

## ROADMAP — единственный источник правды по плану

`ROADMAP.md` — основной трекер прогресса. Работа идёт строго по нему: текущая фаза, её задачи, acceptance criteria.

**Правила синхронизации (обязательны):**

1. **Перед началом работы** — открыть `ROADMAP.md`, найти текущую фазу, проверить, что предыдущая в статусе ✅. Брать задачи только из текущей фазы.
2. **Закрыл задачу — отметь `[x]` сразу же**, в том же изменении, что и код. Не копи отметки пачкой.
3. **Поменялся план в процессе реализации** (решил иначе, добавил/убрал задачу, сменил пакет, отложил пункт) — обнови `ROADMAP.md` в том же коммите, где это произошло. Roadmap не должен расходиться с реальностью ни на один коммит.
4. **Архитектурное решение** (выбор библиотеки, формата, алгоритма) — добавить запись в "Decisions log" в `ROADMAP.md` форматом `[YYYY-MM-DD] [PHASE N] DECISION: ... — RATIONALE: ...` и, если затрагивает архитектуру, синхронизировать `p2p-messenger-design.md`.
5. **Старт фазы** — статус ⏳ → 🚧, проставить `Started` в "Overall progress". **Завершение фазы** — все acceptance criteria зелёные → статус 🚧 → ✅, проставить `Completed`, короткая retrospective-заметка в decisions log.
6. **Не реализуем то, чего нет в roadmap.** Если возникает необходимость — сначала добавить задачу в текущую фазу (или явно отложить в Out-of-MVP), потом делать.

Если roadmap противоречит коду или design doc — это баг. Чини roadmap (или код/doc) до возобновления работы.

---

## Структура репозитория (monorepo)

```
udisend/
├── cmd/
│   ├── network/         # бинарь для запуска узла сети (relay/bootstrap/обычный)
│   │   └── main.go      # минимальный — только парсинг флагов и вызов internal
│   └── messenger/       # бинарь клиента-мессенджера
│       └── main.go      # минимальный — только bootstrap процесса из internal
├── pkg/                 # переиспользуемая логика (потенциально для других проектов)
│   ├── identity/        # Ed25519/X25519, destination hash
│   ├── crypto/          # AEAD, KDF, хеши, обёртки над std lib
│   ├── transport/       # Transport interface, UDP+DTLS реализация
│   ├── packet/          # wire format пакетов, кодеки
│   ├── dht/             # Kademlia / S-Kademlia
│   ├── noise/           # Noise XK link sessions
│   └── ...              # каждый пакет — самостоятельная переиспользуемая единица
├── internal/            # логика, специфичная только для этого проекта
│   ├── network/         # сборка узла сети из pkg-компонентов
│   ├── messenger/       # сборка клиента-мессенджера
│   ├── storage/         # SQLite, outbox, история (специфично для нашего клиента)
│   └── config/          # конфиги обоих бинарей
├── go.mod
├── CLAUDE.md
└── p2p-messenger-design.md
```

### Правила раскладки

1. **`cmd/*` — минимальны.** Только: парсинг флагов/env, инициализация логгера, вызов `internal/<app>.Run(ctx, cfg)`. Ни бизнес-логики, ни сетевых вызовов. Если в `main.go` появляется что-то кроме склейки — переноси в `internal`.

2. **`pkg/` — самодостаточно и переиспользуемо.**
   - Не импортирует `internal/`.
   - Не знает о конкретике этого проекта (никаких упоминаний "messenger", "udisend").
   - Каждый пакет можно вынуть и использовать отдельно.
   - API публичный, документирован, стабильный.

3. **`internal/` — клей и проектная специфика.**
   - Импортирует `pkg/` свободно.
   - Может импортировать другие `internal/*` пакеты.
   - Здесь живёт всё, что не имеет смысла за пределами этого проекта (выбор конкретных дефолтов, конфиг-форматы, склейка компонентов в работающее приложение).

4. **Никаких cyclic deps.** `pkg` ничего не знает про `internal`. `cmd` зависит только от `internal`.

---

## Go version и фишки

- **Go 1.26** — используем последние возможности языка и stdlib.
- Активно применяем:
  - generics там, где это убирает дублирование (контейнеры, kademlia routing table).
  - `iter.Seq` / range-over-func для итераторов (k-buckets, message streams).
  - `slices`, `maps`, `cmp` пакеты вместо ручных циклов.
  - `log/slog` для структурированного логирования (не стандартный `log`, не zap/zerolog без причины).
  - `context.Context` везде, где есть IO или возможность отмены.
  - `errors.Join`, `%w` wrapping, `errors.Is/As`.
  - `sync/atomic` типизированные (`atomic.Pointer[T]`, `atomic.Int64`).
  - `testing/synctest` для детерминированных тестов с временем (Go 1.25+ promoted).
- НЕ используем без явной причины: cgo, reflection, кодогенерация (кроме `stringer` если нужно).

---

## TDD — обязательно

**Каждая фича пишется начиная с теста.** Workflow:

1. Написать failing test, описывающий желаемое поведение.
2. Написать минимальный код, который проходит тест.
3. Отрефакторить, не ломая тесты.
4. Только после этого — следующий тест.

Не пиши production-код без падающего теста на это поведение. Если код невозможно протестировать — это сигнал к рефакторингу архитектуры (DI через interfaces, чистые функции, etc.), а не повод пропустить тест.

### Unit tests — table-driven

Каноничная форма:

```go
func TestDestinationHash(t *testing.T) {
    tests := []struct {
        name    string
        edPub   []byte
        xPub    []byte
        want    [16]byte
        wantErr error
    }{
        {name: "valid keys", ...},
        {name: "empty ed pub", ...},
        ...
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            got, err := DestinationHash(tt.edPub, tt.xPub)
            if !errors.Is(err, tt.wantErr) {
                t.Fatalf("err = %v, want %v", err, tt.wantErr)
            }
            if got != tt.want {
                t.Errorf("got %x, want %x", got, tt.want)
            }
        })
    }
}
```

Правила:
- Параллельные subtests (`t.Parallel()`) везде, где нет shared mutable state.
- Имена кейсов — описывают условие, не реализацию.
- Используем `t.Cleanup` вместо defer для тест-фикстур.
- Хелперы помечаем `t.Helper()`.
- Для случайности — фиксированный seed, либо `math/rand/v2` с seed из теста.

### E2E tests — suite + testcontainers

- Для интеграционных и end-to-end используем suite-стиль (можно `testify/suite` или собственный с `t.Run` группами — выбираем по месту).
- Для зависимостей, которые сложно или дорого мокать, **используем `testcontainers-go`** (`github.com/testcontainers/testcontainers-go`).
- testcontainers применяем **только когда необходимо** (реальная БД, реальный сетевой стек, multi-node DHT). Если задачу решает in-memory fake — используем fake.
- E2E-тесты помечаем build-тегом `//go:build e2e` и/или `testing.Short()` skip, чтобы `go test ./...` оставался быстрым.

```go
//go:build e2e

func (s *DHTSuite) TestNodeDiscoversPeerAcrossContainers() { ... }
```

### Покрытие

- Не гонимся за процентами, но **каждая публичная функция в `pkg/` имеет тесты**.
- Critical paths (крипто, маршрутизация, парсинг wire format) — fuzz-тесты (`testing.F`).
- Concurrency-критичный код — `-race` обязательно в CI.

### Тестируемая архитектура

- Зависимости — через интерфейсы, инжектятся в конструктор.
- Время — через `clockwork.Clock` или собственный `Clock` interface, не `time.Now()` напрямую в логике.
- Случайность — через `io.Reader` (`crypto/rand.Reader` в проде, детерминированный в тесте).
- Сеть — `Transport` interface (см. design doc), in-memory реализация для тестов.

---

## Команды разработки

```bash
go test ./...                    # все юнит-тесты
go test -race ./...              # с гонками
go test -tags=e2e ./...          # включая e2e
go test -fuzz=. -fuzztime=30s ./pkg/packet
go vet ./...
go build ./cmd/...               # оба бинаря
```

---

## Стиль кода

- `gofmt` / `goimports` обязательны.
- Имена пакетов — короткие, lowercase, без подчёркиваний.
- Ошибки — sentinel errors (`var ErrFoo = errors.New(...)`) или typed errors с `As`. Никаких string-сравнений сообщений.
- Никаких `panic` в библиотечном коде (`pkg/`), кроме программерских ошибок (nil constructor args, etc.).
- Комментарии — только когда WHY неочевиден. Документация публичных API — godoc-стиль.

---

## Что НЕ делаем без явного запроса

- Не добавляем зависимости вне списка из design doc (см. секцию "Технологический стек").
- Не пишем README.md, CHANGELOG.md, ARCHITECTURE.md и прочие doc-файлы — есть `p2p-messenger-design.md` и этот `CLAUDE.md`.
- Не делаем feature flags, опциональные конфиги "на будущее", абстракции без второго реального потребителя.
- Не реализуем фичи вне текущего этапа roadmap.

---

## Git commits

- **Не добавляем `Co-Authored-By: Claude ...` или любую другую подпись ассистента** в коммиты, PR-описания, release notes. Авторство — только пользователь.
- Сообщения коммитов на русском или английском по контексту проекта; формат — короткий imperative subject (≤ 72 символа) + опциональный body, объясняющий *почему*.
