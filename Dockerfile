# Build stage: pure-Go SQLite means CGO is never needed.
FROM golang:alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gauge ./cmd/gauge-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gauge /gauge
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8090
# The container has to bind a public address; keep it behind a reverse proxy or
# set GAUGE_AUTH=user:password.
ENV GAUGE_ADDR=0.0.0.0:8090 \
    GAUGE_DB=/data/quota.db \
    GAUGE_KEY=/data/gauge.key \
    GAUGE_ALLOW_PUBLIC=1
ENTRYPOINT ["/gauge"]
