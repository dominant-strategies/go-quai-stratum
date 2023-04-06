FROM golang:1.20-alpine as builder

ADD . /go-quai-stratum

WORKDIR /go-quai-stratum

RUN env GO111MODULE=on go get -v ./...
RUN env GO111MODULE=on go build -o ./build/bin/quai-stratum ./main.go

FROM golang:1.20-alpine

EXPOSE 8008/tcp

COPY --from=builder /go-quai-stratum/build/bin ./build/bin

WORKDIR ./

CMD ./build/bin/quai-stratum config.json
