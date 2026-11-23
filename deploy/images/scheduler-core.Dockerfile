# scheduler-core (separate service and database, ADR-01 option b, K-ARC-02). Build from the repo root:
#   docker build -f deploy/images/scheduler-core.Dockerfile -t scheduler-core:<tag> .
FROM golang:1.27.4-bookworm AS build
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOWORK=off
WORKDIR /src/scheduler-core
COPY scheduler-core/go.mod scheduler-core/go.sum ./
RUN go mod download
COPY scheduler-core/ ./
RUN go build -trimpath -ldflags="-s -w" -o /out/scheduler-core ./cmd/scheduler-core

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/scheduler-core /app/scheduler-core
USER 65532:65532
WORKDIR /app
ENTRYPOINT ["/app/scheduler-core"]
