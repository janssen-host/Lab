FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /controller .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /controller /usr/local/bin/controller
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/controller"]
