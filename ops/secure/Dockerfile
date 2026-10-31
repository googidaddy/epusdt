FROM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS builder
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOMAXPROCS=1 GOMEMLIMIT=768MiB
WORKDIR /build
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/ ./
ARG SOURCE_REVISION=uncommitted-local-validation
RUN go build -p=1 -trimpath -buildvcs=false -ldflags="-s -w -X github.com/GMWalletApp/epusdt/config.BuildCommit=${SOURCE_REVISION}" -o /out/epusdt ./cmd/secure

FROM scratch
ARG SOURCE_REVISION=uncommitted-local-validation
LABEL org.opencontainers.image.title="dujiao dedicated Epusdt" org.opencontainers.image.revision="${SOURCE_REVISION}"
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/epusdt /epusdt
USER 10001:10001
WORKDIR /
ENTRYPOINT ["/epusdt"]
