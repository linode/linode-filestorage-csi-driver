FROM golang:1.26-alpine AS builder

ARG REV=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY pkg ./pkg
COPY internal ./internal

RUN CGO_ENABLED=0 go build -trimpath -ldflags "-w -s -X main.vendorVersion=${REV}" \
    -o /linode-filestorage-csi-driver .

FROM alpine:3.23.3

RUN apk add --no-cache ca-certificates nfs-utils

COPY --from=builder /linode-filestorage-csi-driver /linode-filestorage-csi-driver

ENTRYPOINT ["/linode-filestorage-csi-driver"]
