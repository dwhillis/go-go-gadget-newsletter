.PHONY: all lint test

all: newsletter-arm newsletter-x86

lint: 
	staticcheck .

test:
	go test

newsletter-arm: *.go lint test
	CGO_ENABLED=1 CC_FOR_TARGET=gcc-aarch64-linux-gnu CC=aarch64-linux-gnu-gcc GOARCH=arm64 go build -o newsletter-arm

newsletter-x86: *.go lint test
	go build -ldflags '-extldflags "-static"' -o newsletter-x86
