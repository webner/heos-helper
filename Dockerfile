# Build stage
FROM golang:1.21 as builder

# Set environment variables
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# Set the working directory
WORKDIR /app

# Copy go mod files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN go build -o heos-helper .

# Final stage - smaller image
FROM alpine:latest

# Set the working directory
WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /app/heos-helper .
COPY config.yaml /app/

# Expose port (optional)
EXPOSE 8000

# Start the app
CMD ["./heos-helper"]
