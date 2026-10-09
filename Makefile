.PHONY: run build check
run:
	go run .
build:
	mkdir -p bin
	go build -trimpath -o bin/vsjin-ai .
check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test ./... -count=1
