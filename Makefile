# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: all test clean quai-stratum

GOBIN = ./build/bin
GOGET = env GO111MODULE=on go get
GOTEST = env GO111MODULE=on go test
GOBUILD = env GO111MODULE=on go build

all:
	$(GOGET) -v ./...

test: all
	$(GOTEST) -v ./...

clean:
	env GO111MODULE=on go clean -cache
	rm -fr build/_workspace/pkg/ $(GOBIN)/*

debug:
	$(GOBUILD) -gcflags="all=-N -l" -o ./build/bin/quai-stratum ./main.go

quai-stratum:
	$(GOBUILD) -o ./build/bin/quai-stratum ./main.go