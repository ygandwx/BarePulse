FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY backend ./backend
COPY frontend ./frontend
RUN CGO_ENABLED=0 go build -trimpath -o /barepulse ./backend/cmd/barepulse
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/* && useradd --uid 10001 --create-home barepulse
COPY --from=build /barepulse /usr/local/bin/barepulse
RUN mkdir -p /var/lib/barepulse/media && chown -R 10001:10001 /var/lib/barepulse
USER 10001:10001
WORKDIR /var/lib/barepulse
ENV LISTEN_ADDR=:8080 MEDIA_DIR=/var/lib/barepulse/media
EXPOSE 8080
ENTRYPOINT ["barepulse"]
