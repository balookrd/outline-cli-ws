FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o outline-cli-ws

FROM gcr.io/distroless/base-debian12

WORKDIR /app
COPY --from=builder /app/outline-cli-ws .

USER nonroot:nonroot

ENTRYPOINT ["/app/outline-cli-ws"]
