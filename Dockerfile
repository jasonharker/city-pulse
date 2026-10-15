FROM golang:1.27.2-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /citypulse ./cmd/citypulse

# static: no libc, but CA certificates (the sources are HTTPS) and tzdata.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /citypulse /citypulse
# Numeric, so a pod's runAsNonRoot can verify it.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/citypulse"]
CMD ["serve"]
