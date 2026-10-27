# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.24-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/entregaviva ./cmd/server
# Data directory owned by the distroless "nonroot" user (65532).
RUN mkdir -p /out/data && chown 65532:65532 /out/data

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/entregaviva /entregaviva
COPY --from=build --chown=65532:65532 /out/data /data
ENV PORT=3003 \
    DATABASE_PATH=/data/orders.sqlite
USER 65532:65532
VOLUME ["/data"]
EXPOSE 3003
# No shell or curl in distroless: the binary probes itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/entregaviva", "healthcheck"]
ENTRYPOINT ["/entregaviva"]
