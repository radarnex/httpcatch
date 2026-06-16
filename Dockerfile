# syntax=docker/dockerfile:1.7

FROM golang:1.25.11-alpine@sha256:89f71d90dff0d7f30316963b3c3b8bfe5fb96b94641b3258963ce0c7a21dedda AS build

ARG TARGETARCH
ARG TARGETOS=linux

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=docker
ARG BUILD_TIME=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build,id=go-build-${TARGETARCH} \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/radarnex/httpcatch/internal/buildinfo.Version=${VERSION} \
        -X github.com/radarnex/httpcatch/internal/buildinfo.BuildTime=${BUILD_TIME}" \
      -o /out/httpcatch \
      ./cmd/httpcatch

RUN mkdir -p /out/var/lib/httpcatch

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/radarnex/httpcatch" \
      org.opencontainers.image.description="HTTP traffic capture and inspection tool" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/httpcatch /usr/local/bin/httpcatch
COPY --from=build --chown=nonroot:nonroot /out/var/lib/httpcatch /var/lib/httpcatch

VOLUME ["/var/lib/httpcatch"]

EXPOSE 8080 8081

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/httpcatch"]
CMD ["serve"]
