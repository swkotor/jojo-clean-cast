# Stage 1: Build the Go application
FROM golang:alpine AS builder

WORKDIR /app
COPY . /app
WORKDIR /app/cmd/app
RUN go mod download
RUN CGO_ENABLED=0 go build -o main .

# Stage 2: Create a minimal image with the Go application and ffmpeg
FROM alpine

WORKDIR /app
COPY --from=builder /app/cmd/app/main .
# fork: nodejs is the JS runtime for yt-dlp's YouTube extractor. Without one,
# yt-dlp uses a deprecated no-JS path and YouTube intermittently answers
# 403 Forbidden ("No supported JavaScript runtime could be found").
RUN apk add --no-cache ffmpeg nodejs

ENV PORT=8080
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1
CMD ["./main"]