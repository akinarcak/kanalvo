FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/streamhub ./cmd/streamhub

FROM alpine:3.22
RUN adduser -D -u 10001 streamhub
USER streamhub
COPY --from=build /out/streamhub /usr/local/bin/streamhub
EXPOSE 8000
ENTRYPOINT ["streamhub"]
CMD ["serve"]
