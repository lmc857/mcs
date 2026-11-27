VERSION ?= 0.16.3
BIN := bin

.PHONY: build uasim test run run-https dev-cert linux windows darwin dist package clean fmt vet

build:
	@mkdir -p $(BIN)
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN)/mcs ./cmd/mcs

uasim:
	@mkdir -p $(BIN)
	go build -trimpath -o $(BIN)/uasim ./cmd/uasim

test:
	go test ./...

run: build
	./$(BIN)/mcs -config config.yaml

run-https: build
	./scripts/run-https.sh

run-lan: build
	./scripts/run-lan.sh

dev-cert:
	@mkdir -p data/dev-tls
	openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
	  -keyout data/dev-tls/key.pem -out data/dev-tls/cert.pem \
	  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"

linux:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o dist/mcs-linux-amd64 ./cmd/mcs
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o dist/mcs-linux-arm64 ./cmd/mcs

windows:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o dist/mcs-windows-amd64.exe ./cmd/mcs

darwin:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o dist/mcs-darwin-arm64 ./cmd/mcs
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o dist/mcs-darwin-amd64 ./cmd/mcs

dist: linux windows darwin

package: linux
	@mkdir -p dist/package/mcs
	cp dist/mcs-linux-amd64 dist/package/mcs/mcs
	cp config.example.yaml dist/package/mcs/config.example.yaml
	cp deploy/mcs.service deploy/install-linux.sh dist/package/mcs/
	tar -czf dist/mcs-linux-amd64.tar.gz -C dist/package mcs
	@echo "created dist/mcs-linux-amd64.tar.gz"

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

clean:
	rm -rf $(BIN) dist
