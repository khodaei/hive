BINARY := hive
BUILD_DIR := ./cmd/hive

.PHONY: build fmt test clean install

build: fmt
	go build -o $(BINARY) $(BUILD_DIR)

fmt:
	go fmt ./...

test:
	go test ./cmd/... ./internal/... -v

# Install the binary to $GOBIN (or $GOPATH/bin, usually ~/go/bin).
# Add that dir to PATH once to run `hive` from anywhere.
install:
	go install $(BUILD_DIR)
	@echo "Installed to $$(go env GOBIN 2>/dev/null || echo $$(go env GOPATH)/bin)/$(BINARY)"
	@echo "Make sure that directory is on your PATH."

clean:
	rm -f $(BINARY)
