# Build stage
FROM golang:1.22 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Update go.mod before building
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-w -s" -o /controller cmd/manager/main.go

# Run stage with Alpine
FROM alpine:3.19
# Add CA certificates for HTTPS connections
RUN apk --no-cache add ca-certificates
# Copy the binary from the builder stage
COPY --from=builder /controller /controller
# Use a non-root user for security (Alpine uses different user IDs than distroless)
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
USER appuser
# Set the entrypoint to your controller binary
ENTRYPOINT ["/controller"]