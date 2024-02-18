.PHONY: all lint

all: newsletter-arm newsletter-x86

lint: 
	staticcheck .

newsletter-arm: *.go lint
	CGO_ENABLED=1 CC_FOR_TARGET=gcc-aarch64-linux-gnu CC=aarch64-linux-gnu-gcc GOARCH=arm64 go build -o newsletter-arm

newsletter-x86: *.go lint
	go build -o newsletter-x86
