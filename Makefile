BINARY_NAME=crawler.exe
GO=go

.PHONY: build run test clean fmt vet

build:
	$(GO) build -o $(BINARY_NAME) ./cmd/crawler

run: build
	./$(BINARY_NAME) -seed https://go.dev -depth 2 -workers 5

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY_NAME) crawler
	rm -f data/*.db data/*.db-wal data/*.db-shm
