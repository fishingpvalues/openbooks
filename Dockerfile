# PotatoStack v5.0.0 line: pinned base images (rail: never `latest`),
# non-root distroless runtime, reproducible npm ci for the frontend.
#
# Frontend stage
FROM node:24-alpine AS web
WORKDIR /web
COPY . .
WORKDIR /web/server/app/
RUN npm ci
RUN npm run build

# Go build stage: go1.26.6 so the binary carries no known-vulnerable
# standard library (16 reachable advisories were in 1.26.0).
FROM golang:1.26.6-alpine AS build
WORKDIR /go/src/
COPY . .
COPY --from=web /web/ .

ENV CGO_ENABLED=0
RUN go get -d -v ./...
RUN go install -v ./...
WORKDIR /go/src/cmd/openbooks/
RUN go build -o openbooks .

# Runtime: distroless static. No shell, no package manager, no su to root.
# The base image's own `USER nonroot` (uid 65532) is overridden below:
# the library bind (/mnt/storage2/downloads/books-annas -> /books) is
# owned by 1000:1000 (hermes) and is also bookdl's staging dir - bookdl
# runs as 1000. The upstream image ran as root, which worked only by
# bypassing those permissions; 1000 is the stack convention for
# library writers and keeps this service non-root.
FROM gcr.io/distroless/static:nonroot AS app
USER 1000:1000
WORKDIR /app
COPY --from=build /go/src/cmd/openbooks/openbooks .

EXPOSE 80
VOLUME [ "/books" ]
ENV BASE_PATH=/

# The image must listen on all its interfaces (network_mode service:gluetun);
# the binary defaults to 127.0.0.1 on purpose, so 0.0.0.0 is passed here.
# Token comes from the environment (OPENBOOKS_TOKEN).
ENTRYPOINT ["./openbooks", "server", "--dir", "/books", "--port", "80", "--bind", "0.0.0.0"]
