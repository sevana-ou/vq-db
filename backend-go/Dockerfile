# Build a static, CGo-free vq-db binary and ship it on distroless.
# Build context is backend-go/.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 keeps the binary static (modernc sqlite + go-zeromq are pure Go).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/vq-db ./cmd/vq-db

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/vq-db /usr/local/bin/vq-db
# Dashboard static files and config are mounted at runtime.
#   docker run -v /etc/vq-monitor:/etc/vq-monitor -v /var/vq-monitor:/var/vq-monitor \
#     --network host image --config /etc/vq-monitor/vq-monitor.cfg
ENTRYPOINT ["/usr/local/bin/vq-db"]
