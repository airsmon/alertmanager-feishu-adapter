# syntax=docker/dockerfile:1

ARG GO_VERSION=1.25.0
FROM golang:${GO_VERSION}-bookworm AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .

RUN go test ./...

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/adapter ./cmd/adapter

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/adapter /adapter

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/adapter"]
