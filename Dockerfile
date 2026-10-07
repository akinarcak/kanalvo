FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# vite.config.ts çıktıyı ../internal/panelui/dist içine yazar.
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/panelui/dist ./internal/panelui/dist
RUN CGO_ENABLED=0 go build -o /out/kanalvo ./cmd/kanalvo

FROM alpine:3.22
RUN adduser -D -u 10001 kanalvo
USER kanalvo
COPY --from=build /out/kanalvo /usr/local/bin/kanalvo
EXPOSE 8000 8002
ENTRYPOINT ["kanalvo"]
CMD ["serve"]
