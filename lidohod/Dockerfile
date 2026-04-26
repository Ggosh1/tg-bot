# Этап 1: Сборка бинарников
FROM golang:1.25-alpine AS builder

# Устанавливаем gcc для SQLite (CGO)
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# Копируем управление зависимостями
COPY go.mod go.sum ./
RUN go mod download

# Копируем весь код проекта
COPY . .

# Собираем оба приложения
RUN CGO_ENABLED=1 GOOS=linux go build -o /build/bin/tg-front ./tg-front
RUN CGO_ENABLED=0 GOOS=linux go build -o /build/bin/tg-parser ./tg-parser

# Этап 2: Финальный образ для работы
FROM alpine:latest

# Сертификаты для HTTPS и таймзона
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Забираем только готовые бинарники из первого этапа
COPY --from=builder /build/bin/tg-front .
COPY --from=builder /build/bin/tg-parser .

# Создаем папку для базы и сессии
RUN mkdir -p /app/data