FROM golang:1.27.2-alpine

WORKDIR /app
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY tests/api ./tests/api
RUN go build -o /usr/local/bin/billing-api ./cmd/billing-api

ENV GOCACHE=/tmp/go-build
USER 65532:65532
EXPOSE 8080
CMD ["billing-api"]
