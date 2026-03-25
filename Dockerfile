FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG GH_PAT

# Configure Git for private modules
RUN apk add --no-cache git && \
    git config --global url."https://${GH_PAT}:x-oauth-basic@github.com/dennisschroeder".insteadOf "https://github.com/dennisschroeder"

ENV GOPRIVATE=github.com/dennisschroeder/*

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-w -s" -o /service .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /service /service
ENTRYPOINT ["/service"]
