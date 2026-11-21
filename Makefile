.PHONY: all build clean test run release

BINARY_NAME=pycloud
DIST_DIR=dist

all: test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY_NAME) .

test:
	go test -v ./...

run: build
	./$(BINARY_NAME)

release: clean test
	@mkdir -p $(DIST_DIR)
	@echo "Compiling Linux (amd64)..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-linux-amd64 .
	@echo "Compiling Linux ARM64 (Raspberry Pi)..."
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-linux-arm64 .
	@echo "Compiling Windows (amd64)..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe .
	@echo "Compiling macOS Apple Silicon (arm64)..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 .
	@echo "Compiling macOS Intel (amd64)..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 .
	@echo "Done! Binaries available in $(DIST_DIR)/"

clean:
	rm -f $(BINARY_NAME)
	rm -rf $(DIST_DIR)
