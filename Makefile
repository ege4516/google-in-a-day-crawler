GO=go

# Cross-platform detection
ifeq ($(OS),Windows_NT)
    BINARY_NAME=crawler.exe
    RM=cmd /C del /Q
    SEP=\\
    NULL=NUL
else
    BINARY_NAME=crawler
    RM=rm -f
    SEP=/
    NULL=/dev/null
endif

.PHONY: build run test clean fmt vet

build:
	$(GO) build -o $(BINARY_NAME) ./cmd/crawler

run:
	$(GO) run ./cmd/crawler

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	$(GO) clean
	-$(RM) $(BINARY_NAME) 2>$(NULL)
	-$(RM) data$(SEP)*.db 2>$(NULL)
	-$(RM) data$(SEP)*.db-wal 2>$(NULL)
	-$(RM) data$(SEP)*.db-shm 2>$(NULL)
