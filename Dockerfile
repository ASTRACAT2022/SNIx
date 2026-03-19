FROM golang:1.21-alpine AS builder

WORKDIR /app

# Установить зависимости
COPY go.mod go.sum ./
RUN go mod download 2>/dev/null || true

# Скопировать исходный код
COPY . .

# Скомпилировать
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-s -w" -o sni-proxy .

# Финальный образ
FROM alpine:latest

# Установить корневые сертификаты
RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Скопировать бинарный файл и конфиг
COPY --from=builder /app/sni-proxy .
COPY --from=builder /app/config.json .

# Открыть порты
EXPOSE 443 9339 30000

# Запуск
CMD ["./sni-proxy"]
