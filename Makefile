linux-amd64: vet
	GOOS=linux GOARCH=amd64 go build -o sdrconnect-scanner

linux-arm64: vet
	GOOS=linux GOARCH=arm64 go build -o sdrconnect-scanner

windows-amd64: vet
	GOOS=windows GOARCH=amd64 go build -o sdrconnect-scanner.exe

darwin-arm64: vet
	GOOS=darwin GOARCH=arm64 go build -o sdrconnect-scanner

fmt:
	go fmt ./...

lint: fmt
	golint ./...

vet: fmt
	go vet ./...
