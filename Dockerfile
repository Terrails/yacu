# yacu is pure Go, so it is built on the build machine's platform for the target one
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/yacu ./main

FROM alpine:3.22

WORKDIR /data

# certificates to reach registries over HTTPS, and time zones for TZ
RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Europe/London

COPY --from=build /out/yacu /usr/local/bin/yacu

ENTRYPOINT ["/usr/local/bin/yacu"]
