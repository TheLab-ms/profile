FROM golang:1.21 AS builder
WORKDIR /app
RUN curl -L "https://github.com/FiloSottile/age/releases/download/v1.1.1/age-v1.1.1-linux-amd64.tar.gz" | tar -zx
ADD go.mod .
ADD go.sum .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build

FROM scratch
COPY --from=builder /app/profile-server /profile-server
COPY --from=builder /app/age/age /bin/age
ENV PATH=/bin
ENTRYPOINT ["/profile-server"]
