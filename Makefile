BIN := $(HOME)/.hydra/bin/hydra

.PHONY: build install run vet test snapshot clean

## build: compile hydra to ~/.hydra/bin/hydra
build:
	@mkdir -p $(dir $(BIN))
	go build -o $(BIN) ./cmd/hydra

## install: build + wire Claude Code hooks (local dev install)
install: build
	$(BIN) install
	@echo "Add ~/.hydra/bin to PATH, then run: hydra"

## run: build and open the console
run: build
	$(BIN)

vet:
	go vet ./...

test:
	go test ./...

## snapshot: build cross-platform release archives locally (needs goreleaser)
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -f $(BIN)
