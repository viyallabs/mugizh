# Core API, worker and migrate in one image (S-094; the same image per release:
# docs/ops/production-readiness.md section 4). Build from the repo root:
#   docker build -f deploy/images/core.Dockerfile -t core:<tag> .
# Run as: /app/api, /app/worker or /app/migrate up. Synthetic data only outside production.
FROM golang:1.27.4-bookworm AS build
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOWORK=off
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker \
 && go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# Final image: no shell, no package manager, no root (TM-45, TM-47; Trivy AVD-DS-0002).
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ /app/
# Country packs and the vendor register are read at start-up (checksums verified, K-GLB-01, K-DP-08).
COPY config/country-packs/ /app/config/country-packs/
COPY config/vendor-register.yaml /app/config/vendor-register.yaml
ENV MUGIZH_PACK_DIR=/app/config/country-packs/IN MUGIZH_VENDOR_REGISTER=/app/config/vendor-register.yaml
USER 65532:65532
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["/app/api"]
