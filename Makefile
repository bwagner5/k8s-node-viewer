BIN := knv

.PHONY: build test race lint fmt demo frames clean

build:
	go build -o $(BIN) ./cmd/knv

test:
	go test ./...

race:
	go test -race ./...

fmt:
	gofmt -w .

lint:
	go vet ./...

# Run against the simulated cluster.
demo: build
	./$(BIN) --demo

# Dump plain-text frames for eyeballing layout without a terminal.
frames:
	KNV_DUMP=/tmp/knv-frames.txt go test ./internal/ui/ -run TestDumpFrames
	@echo "wrote /tmp/knv-frames.txt"

clean:
	rm -f $(BIN)
