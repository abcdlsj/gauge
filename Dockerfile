# Build stage: pure-Go SQLite means CGO is never needed.
FROM golang:alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gpanel ./cmd/gpanel

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gpanel /gpanel
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8090
ENV GPANEL_ADDR=0.0.0.0:8090 \
    GPANEL_DB=/data/quota.db \
    GPANEL_KEY=/data/gpanel.key
ENTRYPOINT ["/gpanel"]
