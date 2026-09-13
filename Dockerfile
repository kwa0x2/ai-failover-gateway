FROM golang:1.26.3-alpine AS build

WORKDIR /src

# Dependencies change far less often than source, so they get their own layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off makes the binary static, which is what lets the runtime stage be
# distroless/static with no libc at all. -s -w drop the symbol and DWARF
# tables; the gateway is debugged through logs and metrics, not core dumps.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot

# Copied for the outbound TLS handshake to api.anthropic.com and Bedrock.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/gateway /gateway

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/gateway"]
