FROM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS builder
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
