BINARY_NAME=crawler
GO=go
GOFLAGS=-race

.PHONY: build run test clean fmt vet

build:
	$(GO) build -o $(BINARY_NAME) ./cmd/crawler

run: build
	./$(BINARY_NAME) -seed https://go.dev -depth 2 -workers 5

test:
	$(GO) test $(GOFLAGS) ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME).exe
	rm -f data/*.db data/*.db-wal data/*.db-shm
