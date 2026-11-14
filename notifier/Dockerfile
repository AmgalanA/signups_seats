# Build stage: compile a static binary.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/notifier .

# Run stage: small image with only the binary.
FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/notifier /usr/local/bin/notifier
EXPOSE 8081
ENTRYPOINT ["notifier"]
