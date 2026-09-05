FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
COPY cmd/sdl-db-backup-validate ./cmd/sdl-db-backup-validate
RUN CGO_ENABLED=0 go build -o /out/sdl-db-backup . && \
    CGO_ENABLED=0 go build -o /out/sdl-db-backup-validate ./cmd/sdl-db-backup-validate

# Ubuntu supplies Oracle MySQL 8 clients (MariaDB's mysqldump lacks required flags)
# and PostgreSQL 16 clients, matching the servers below.
FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates mysql-client postgresql-client-16 python3 python3-boto3 && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ /usr/local/bin/
COPY tests/integration/suite.py /suite/suite.py
WORKDIR /suite
ENV PYTHONUNBUFFERED=1
CMD ["python3", "/suite/suite.py"]
