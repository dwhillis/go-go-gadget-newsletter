.PHONY: all lint test clean

all: newsletter-arm newsletter-x86

lint: 
	staticcheck .

test:
	go test ./...

newsletter-arm: *.go lint test
	CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc GOARCH=arm64 go build -o newsletter-arm

newsletter-x86: *.go lint test
	go build -o newsletter-x86

clean:
	rm -f newsletter-arm newsletter-x86
