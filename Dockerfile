# Stage 1: Build the Go application
FROM golang:alpine AS builder

WORKDIR /app
COPY . /app
WORKDIR /app/cmd/app
RUN go mod download
RUN CGO_ENABLED=0 go build -o main .

# Stage 1b: whisper.cpp, for finding HOST-READ ads.
# Those are spoken by the hosts and baked into the master recording, so they are
# identical in every download and the differential stripper cannot see them. The
# only way to locate them is to read what is being said.
# Built STATIC (-DBUILD_SHARED_LIBS=OFF): one self-contained binary, rather than
# a set of .so files to keep in sync with it.
FROM alpine AS whisperbuild
RUN apk add --no-cache build-base cmake git
RUN git clone --depth 1 https://github.com/ggml-org/whisper.cpp /w
WORKDIR /w
RUN cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
      -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=ON -DWHISPER_BUILD_SERVER=OFF \
 && cmake --build build -j"$(nproc)" --target whisper-cli

# Stage 2: Create a minimal image with the Go application and ffmpeg
FROM alpine

WORKDIR /app
COPY --from=builder /app/cmd/app/main .
# fork: nodejs is the JS runtime for yt-dlp's YouTube extractor. Without one,
# yt-dlp uses a deprecated no-JS path and YouTube intermittently answers
# 403 Forbidden ("No supported JavaScript runtime could be found").
RUN apk add --no-cache ffmpeg nodejs libstdc++ libgomp
COPY --from=whisperbuild /w/build/bin/whisper-cli /usr/local/bin/whisper-cli

ENV PORT=8080
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1
CMD ["./main"]