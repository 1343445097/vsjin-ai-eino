ARG GO_BUILDER_IMAGE=golang:1.24.9-alpine
ARG RUNTIME_IMAGE=alpine:3.22
FROM ${GO_BUILDER_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOMAXPROCS=2 CGO_ENABLED=0 go build -p 2 -trimpath -ldflags="-s -w" -o /vsjin-ai .

FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 vsjin \
    && adduser -S -u 10001 -G vsjin vsjin \
    && mkdir /data && chown vsjin:vsjin /data
COPY --from=build /vsjin-ai /usr/local/bin/vsjin-ai
COPY --from=build /src/LICENSE /src/THIRD_PARTY_NOTICES.md /usr/share/licenses/vsjin-ai/
USER 10001:10001
ENV VSJIN_AI_HOST=0.0.0.0 VSJIN_AI_PORT=18900 VSJIN_AI_DATA_DIR=/data
EXPOSE 18900
VOLUME ["/data"]
ENTRYPOINT ["vsjin-ai"]
