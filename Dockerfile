# syntax=docker/dockerfile:1

# ---- builder stage ----
FROM golang:1.25-alpine AS builder
WORKDIR /src

# cache module downloads
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/mqtt2ha .

# ---- runtime stage ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata

# run as unprivileged user
RUN addgroup -S mqtt2ha && adduser -S -G mqtt2ha -h /app mqtt2ha \
    && mkdir -p /app/data && chown mqtt2ha:mqtt2ha /app/data

COPY --from=builder /out/mqtt2ha /usr/local/bin/mqtt2ha

USER mqtt2ha
# config.yaml + mqtt2ha.db live here; mount a volume for persistence
WORKDIR /app/data
VOLUME ["/app/data"]

EXPOSE 8080
ENTRYPOINT ["mqtt2ha"]
CMD ["-config", "/app/data/config.yaml"]
