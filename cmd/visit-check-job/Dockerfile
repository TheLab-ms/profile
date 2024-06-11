FROM golang:1.21 AS builder
WORKDIR /app
ADD go.mod .
ADD go.sum .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build ./cmd/visit-check-job

FROM scratch
COPY --from=builder /app/visit-check-job /visit-check-job
ENTRYPOINT ["/visit-check-job"]
