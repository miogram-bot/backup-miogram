FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

COPY go.mod go.sum ./

ENV GOPROXY=https://mirrors.aliyun.com/goproxy/,direct
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o miogram ./cmd/miogram

FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata curl

# نصب cloudflared
RUN curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared && \
    chmod +x /usr/local/bin/cloudflared

ENV TZ=Asia/Tehran

WORKDIR /app

COPY --from=builder /app/miogram .
COPY --from=builder /app/bot/files ./bot/files

# Self-signed certificate pinned to Telegram webhooks (setWebhook certificate field).
# Also bind-mounted at runtime via docker-compose for hot updates without rebuild.
COPY ssl/cert.pem /app/ssl/cert.pem

EXPOSE 8080

CMD ["./miogram"]
