export GO111MODULE=on

RELEASE_PATH = dist

install:
	go mod verify && go mod download

build:
	GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w" -o ${RELEASE_PATH}/gw-macos ./cmd && \
	GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o ${RELEASE_PATH}/gw ./cmd && \
	GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o ${RELEASE_PATH}/gw.exe ./cmd

