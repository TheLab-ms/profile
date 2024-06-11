FROM golang:1.21 AS builder
WORKDIR /app
ADD go.mod .
ADD go.sum .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build ./cmd/paypal-check-job

FROM scratch
COPY --from=builder /app/paypal-check-job /paypal-check-job
ENTRYPOINT ["/paypal-check-job"]
