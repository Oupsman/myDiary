# syntax=docker/dockerfile:1

# Builds the sync server only: the desktop app needs GTK and is not deployed.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/mydiary-server ./cmd/mydiary-server \
 && install -d -m 0700 /out/data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/mydiary-server /usr/local/bin/mydiary-server
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME /data
EXPOSE 8484
USER nonroot:nonroot
ENTRYPOINT ["mydiary-server", "-data", "/data"]
CMD ["serve", "-listen", ":8484"]
