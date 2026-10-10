BINARY := backstop
PKG := ./...

.PHONY: build test race vet fmt check run docker-up docker-down demo clean

build:
	go build -o bin/$(BINARY) ./cmd/backstop

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet $(PKG)

fmt:
	gofmt -l -w .

check: fmt vet race

run: build
	./bin/$(BINARY) run

docker-up:
	docker compose up -d --wait

docker-down:
	docker compose down -v

demo: build
	./scripts/demo.sh

clean:
	rm -rf bin
