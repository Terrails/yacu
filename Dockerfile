FROM golang:1.21-alpine

WORKDIR /app

# install build dependencies
RUN apk add --no-cache tzdata build-base lvm2-dev btrfs-progs-dev gpgme-dev
ENV TZ=Europe/London

# copy all files into container
COPY . ./

# install go dependencies
RUN go mod download

# compile
RUN CGO_ENABLED=1 go build -o /yacu /app/main

# change workdir to folder that should be mounted
WORKDIR /data

CMD ["/yacu"]