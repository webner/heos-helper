# Build stage
FROM golang:1.27 AS builder

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -trimpath -ldflags="-s -w" -o heos-helper .

# Final stage. Pin the Alpine release: alpine:latest only changes when the
# image is rebuilt, so it never picked up security fixes on its own anyway.
FROM alpine:3.24.1

WORKDIR /app

COPY --from=builder /app/heos-helper .
COPY config.yaml /app/

# The helper only reads config.yaml and talks TCP to the speakers.
USER 65532:65532

EXPOSE 8000

CMD ["./heos-helper"]
