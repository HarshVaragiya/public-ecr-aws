FROM golang:buster as builder
WORKDIR /app
COPY . /app/
ENV CGO_ENABLED 0
RUN mkdir -p /app/bin/ && go build -o bin/ecrgallery .

FROM alpine
WORKDIR /app
COPY --from=builder /app/bin/ecrgallery /app/ecrgallery
ENTRYPOINT ["/app/ecrgallery"]