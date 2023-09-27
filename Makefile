.PHONY: all
all: newsletter-arm newsletter-x86

newsletter-arm: *.go
	CGO_ENABLED=1 CC_FOR_TARGET=gcc-aarch64-linux-gnu CC=aarch64-linux-gnu-gcc GOARCH=arm64 go build -o newsletter-arm

newsletter-x86: *.go
	go build -o newsletter-x86
