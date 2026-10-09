FROM golang:1.27.2-alpine

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY tests/api ./tests/api
COPY tests/inbox ./tests/inbox
COPY tests/worker ./tests/worker
COPY migrations ./migrations
RUN go build -o /usr/local/bin/billing-api ./cmd/billing-api
RUN go build -o /usr/local/bin/billing-worker ./cmd/billing-worker

ENV GOCACHE=/tmp/go-build
USER 65532:65532
EXPOSE 8080
CMD ["billing-api"]
