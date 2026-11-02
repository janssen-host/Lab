FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -o /out/example-project .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/example-project /example-project
EXPOSE 8080
ENTRYPOINT ["/example-project"]
