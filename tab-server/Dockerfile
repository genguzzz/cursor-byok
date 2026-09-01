# syntax=docker/dockerfile:1.7

FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY tab-server/go.mod tab-server/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY tab-server/src/ src/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/cursor-tab-server ./src

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cursor-tab-server /usr/local/bin/cursor-tab-server
# The image ships no token. Mount config.yaml at /etc/cursor-tab-server/config.yaml.
WORKDIR /etc/cursor-tab-server
USER nonroot:nonroot
EXPOSE 8041
ENTRYPOINT ["/usr/local/bin/cursor-tab-server", "-config", "/etc/cursor-tab-server/config.yaml"]
