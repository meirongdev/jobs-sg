GO ?= go
BIN_DIR := bin
CMD := ingest enrich report web

.PHONY: all build test vet fmt tidy clean fixture
all: build

build:
	@mkdir -p $(BIN_DIR)
	@for c in $(CMD); do CGO_ENABLED=0 $(GO) build -o $(BIN_DIR)/jobs-sg-$$c ./cmd/$$c; done

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR) data report
