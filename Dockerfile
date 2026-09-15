FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /bifrost-datahub-exporter ./cmd/exporter
RUN mkdir -m 0777 /state

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /bifrost-datahub-exporter /bifrost-datahub-exporter
COPY --from=build --chown=65534:65534 /state /state
USER 65534:65534
ENV STATE_FILE=/state/state.json
ENTRYPOINT ["/bifrost-datahub-exporter"]
