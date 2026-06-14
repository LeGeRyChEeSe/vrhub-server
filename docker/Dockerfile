# syntax=docker/dockerfile:1

# ---- build stage ----------------------------------------------------------
FROM golang:1.26 AS build

WORKDIR /src

# Cache module downloads first.
COPY go.mod go.sum ./
RUN go mod download

# Build the static binary. modernc.org/sqlite is pure Go, so CGO is off and
# the result is a fully static executable with no external dependencies.
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w" -o /out/vrhub-server ./cmd/server/

# ---- runtime stage --------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/vrhub-server /usr/local/bin/vrhub-server

# The data directory is mounted as a volume so the SQLite database, config,
# metadata cache and backups survive container restarts.
VOLUME ["/data"]

EXPOSE 39457

ENTRYPOINT ["/usr/local/bin/vrhub-server", "-data-dir", "/data"]
