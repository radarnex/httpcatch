# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=docker
ARG BUILD_TIME=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/radarnex/httpcatch/internal/buildinfo.Version=${VERSION} \
        -X github.com/radarnex/httpcatch/internal/buildinfo.BuildTime=${BUILD_TIME}" \
      -o /out/httpcatch \
      ./cmd/httpcatch

RUN mkdir -p /out/var/lib/httpcatch

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/httpcatch /usr/local/bin/httpcatch
COPY --from=build --chown=nonroot:nonroot /out/var/lib/httpcatch /var/lib/httpcatch

VOLUME ["/var/lib/httpcatch"]

EXPOSE 8080 8081

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/httpcatch"]
CMD ["serve"]
