# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: all test clean

GOBIN = ./build/bin
GOGET = env GO111MODULE=on go get
GOTEST = env GO111MODULE=on go test

all:
	build/env.sh $(GOGET) -v ./...

test: all
	build/env.sh $(GOTEST) -v ./...

clean:
	env GO111MODULE=on go clean -cache
	rm -fr build/_workspace/pkg/ $(GOBIN)/*
