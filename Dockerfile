FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pgtun ./cmd/pgtun

FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
    && addgroup -S pgtun \
    && adduser -S -G pgtun -H pgtun
COPY --from=build /out/pgtun /usr/local/bin/pgtun

USER pgtun
ENTRYPOINT ["pgtun"]
