FROM golang:1.26-alpine AS build

WORKDIR /src

# Build dependencies required by CGO-enabled modules.
RUN apk add --no-cache build-base lvm2-dev btrfs-progs-dev gpgme-dev

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/yacu ./main

FROM alpine:3.22

WORKDIR /data

# Runtime-only libs for the linked binary.
RUN apk add --no-cache ca-certificates tzdata sqlite-libs gpgme libstdc++ lvm2-libs btrfs-progs
ENV TZ=Europe/London

COPY --from=build /out/yacu /usr/local/bin/yacu

ENTRYPOINT ["/usr/local/bin/yacu"]