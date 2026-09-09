BIN := bin/tracker
DATA ?= ./data

.PHONY: build test vet fmt run clean docker

build:
	go build -trimpath -o $(BIN) ./cmd/tracker

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Runs against a local data directory. Drop .gpx files into $(DATA)/inbox and
# press Refresh; no cloud remote is needed for local work.
run: build
	DATA_DIR=$(DATA) APP_TZ=$${APP_TZ:-Europe/Warsaw} PORT=$${PORT:-8381} $(BIN)

docker:
	docker build -t routine-routes-tracker .

clean:
	rm -rf bin
