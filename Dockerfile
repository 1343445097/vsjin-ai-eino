FROM golang:1.24.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /vsjin-ai .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S vsjin && adduser -S -G vsjin vsjin && mkdir /data && chown vsjin:vsjin /data
COPY --from=build /vsjin-ai /usr/local/bin/vsjin-ai
USER vsjin
ENV VSJIN_AI_HOST=0.0.0.0 VSJIN_AI_PORT=18900 VSJIN_AI_DATA_DIR=/data
EXPOSE 18900
VOLUME ["/data"]
ENTRYPOINT ["vsjin-ai"]
