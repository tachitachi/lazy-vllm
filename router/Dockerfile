FROM golang:1.26.2-alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY *.go ./

RUN go build -ldflags="-s -w" -o thinking-router .

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /app/thinking-router .

EXPOSE 8001

CMD ["./thinking-router"]
