FROM golang:1.26.2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/btw-api ./service/btw/api && CGO_ENABLED=0 go build -o /out/btw-worker ./service/async/rpc/cmd/worker

FROM gcr.io/distroless/static-debian12:nonroot AS api
WORKDIR /app
COPY --from=build /out/btw-api /app/btw-api
COPY service/btw/api/etc/btw-api.yaml /app/etc/btw-api.yaml
ENTRYPOINT ["/app/btw-api", "-f", "/app/etc/btw-api.yaml"]

FROM gcr.io/distroless/static-debian12:nonroot AS worker
WORKDIR /app
COPY --from=build /out/btw-worker /app/btw-worker
COPY service/btw/api/etc/btw-api.yaml /app/etc/btw-api.yaml
ENTRYPOINT ["/app/btw-worker", "-f", "/app/etc/btw-api.yaml"]
