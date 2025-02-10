# Этап сборки
FROM golang:1.23-alpine AS builder
WORKDIR /app

# Копируем файлы модулей и скачиваем зависимости
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходники и собираем бинарник (статическая сборка)
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o messenger .

# Итоговый минимальный образ
FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/messenger .

# Открываем порты, необходимые для работы (например, 5000, 6000, 7000, 8000, 9000)
EXPOSE 5000 6000 7000 8000 9000

# Запускаем приложение, используя переменные окружения NICK, PORT, PARENT
CMD ["sh", "-c", "./messenger --nick=${NICK} --port=${PORT} ${PARENT:+--parent=${PARENT}}"]

