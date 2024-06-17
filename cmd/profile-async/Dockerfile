FROM golang:1.21 AS builder
WORKDIR /app
ADD go.mod .
ADD go.sum .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build ./cmd/profile-async

FROM scratch
COPY --from=builder /app/profile-async /profile-async
ENV PATH=/bin
ENTRYPOINT ["/profile-async"]
