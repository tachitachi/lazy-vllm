.PHONY: build run test test-coverage lint

build:
	go build -o lazy-vllm-proxy .

run:
	go run .

test:
	go test -count=1 ./...

test-coverage:
	go test -count=1 -coverprofile=/tmp/proxy-cover.out ./... && go tool cover -html=/tmp/proxy-cover.out

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.3 run
