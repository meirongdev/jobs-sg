# Multi-stage build: statically-linked binaries, distroless-style runtime
# (docs/02 §3: single static binary, ~10MB, multi-arch).
# Build with: docker build --platform linux/amd64,linux/arm64 -t ghcr.io/meirongdev/jobs-sg .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/jobs-sg-ingest  ./cmd/ingest && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/jobs-sg-enrich  ./cmd/enrich && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/jobs-sg-report  ./cmd/report && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/jobs-sg-web     ./cmd/web && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/jobs-sg-reclassify ./scripts/reclassify

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/jobs-sg-ingest  /usr/local/bin/
COPY --from=build /out/jobs-sg-enrich  /usr/local/bin/
COPY --from=build /out/jobs-sg-report  /usr/local/bin/
COPY --from=build /out/jobs-sg-web     /usr/local/bin/
# reclassify is an operator tool, not a pipeline stage — no CronJob runs it.
# It ships in the image because the only place it can run is against the mounted
# PVC: the archive it replays is hundreds of MB on the node's local-path volume,
# so "run it locally against a copy" is not a real option. Invoke it as a
# one-off Job (`--data-dir /data`, dry run unless given --apply).
COPY --from=build /out/jobs-sg-reclassify /usr/local/bin/
# non-root, read-only root FS friendly; /data is the mounted PVC
USER 10001
WORKDIR /data
EXPOSE 8080
