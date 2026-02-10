# Build stage
FROM golang:1.25-alpine AS builder

# Install git and ca-certificates
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-s -w" -o webring cmd/server/main.go

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /build/webring .
COPY --from=builder /build/docs ./docs
COPY --from=builder /build/messages ./messages

# Create media directory
RUN mkdir -p media

# Expose port
EXPOSE 8080

# Set default environment variables
ENV PORT=8080
ENV MEDIA_FOLDER=media

# Run the binary
CMD ["./webring"]