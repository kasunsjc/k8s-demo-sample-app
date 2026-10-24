FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/inspector-api .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/inspector-api /inspector-api
# Numeric UID so kubelet can verify runAsNonRoot.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/inspector-api"]
