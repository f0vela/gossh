.PHONY: build run test install
build:
	go build -trimpath -ldflags "-s -w" -o dist/gossh .
run:
	go run .
test:
	go vet ./... && go test ./...
install:
	go install -trimpath .
