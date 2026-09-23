FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /treetest ./cmd/treetest
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /treetest /treetest
COPY --from=build /src/studies /studies
COPY --chown=65532:65532 --from=build /data /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/treetest"]
CMD ["serve", "--db", "/data/treetest.sqlite", "--listen", "0.0.0.0:8080"]
