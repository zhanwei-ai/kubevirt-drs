# Build: podman build -t ghcr.io/zhanwei-ai/kubevirt-drs:dev --build-arg VERSION=dev .
FROM docker.io/library/golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/zhanwei-ai/kubevirt-drs/internal/version.Version=${VERSION}" \
      -o /out/manager ./cmd/manager

# Static binary on distroless: no shell, no package manager, nothing for a CVE scanner to find.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
