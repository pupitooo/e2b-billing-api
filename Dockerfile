FROM public.ecr.aws/docker/library/golang:1.27.2-alpine AS go-tools

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY scripts/whitespace/go.mod scripts/whitespace/go.sum ./scripts/whitespace/
RUN go build -mod=readonly -modfile=./scripts/whitespace/go.mod \
    -o /usr/local/bin/e2b-wsl github.com/bombsimon/wsl/v5/cmd/wsl

FROM go-tools AS api
COPY cmd ./cmd
COPY internal ./internal
COPY tests/api ./tests/api
COPY tests/inbox ./tests/inbox
COPY tests/worker ./tests/worker
COPY migrations ./migrations
COPY docs/simulator ./docs/simulator
RUN go build -o /usr/local/bin/billing-api ./cmd/billing-api
RUN go build -o /usr/local/bin/billing-worker ./cmd/billing-worker
RUN go build -o /usr/local/bin/platform-simulator ./cmd/platform-simulator

# Initialize the persistent sender volume for the same unprivileged runtime.
RUN mkdir -p /state && chown 65532:65532 /state

ENV GOCACHE=/tmp/go-build
USER 65532:65532
EXPOSE 8080
CMD ["billing-api"]
