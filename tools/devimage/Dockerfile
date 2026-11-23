# Dev and CI tool image (S-091). Pinned versions; rebuild with `task devimage`.
# Used locally (Windows Application Control blocks unsigned Go tool binaries on the founder's laptop)
# and as the GitLab CI job image.
FROM golang:1.27.4-bookworm
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly GOTOOLCHAIN=local
RUN go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.2 \
 && go install golang.org/x/vuln/cmd/govulncheck@v1.9.0 \
 && go install github.com/securego/gosec/v2/cmd/gosec@v2.30.0 \
 && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 \
 && rm -rf /root/.cache/go-build /go/pkg/mod
WORKDIR /src
