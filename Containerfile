FROM golang:1.26.2-alpine3.23 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN --mount=type=bind,target=. \
	go build -trimpath -ldflags='-s -w' -o /out/tsgo ./cmd/tsgo

FROM busybox:musl
COPY --from=build /out/tsgo /usr/local/bin/tsgo
WORKDIR /src
ENTRYPOINT ["/usr/local/bin/tsgo"]
