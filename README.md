# Keel

`keel` turns your containerized app into a UDS package.

If you already know how to build containers, `keel` helps you ship to Kubernetes without writing Kubernetes YAML.

It reads either Dockerfile instructions or a Docker Compose file and generates a deployable `.dist/` package for Zarf/UDS.

## Commands

- `keel gen [PATH]`: Parse Dockerfile (default) or Docker Compose, render manifests + `zarf.yaml`, build OCI image archive(s), and validate output.
- `keel version`: Print the CLI version.

## Quickstart

```bash
# Build the Keel binary
go build -o ./build/keel .

# Generate from Dockerfile
./build/keel gen examples/Dockerfile

# Generate from Docker Compose
./build/keel gen examples/docker-compose.yml

# Package with Zarf
zarf package create .dist

# (Optionally) Deploy k3d core slim dev and the package
uds deploy k3d-core-slim-dev:latest
zarf package deploy --confirm zarf-package-hello-world-*-0.1.0.tar.zst
```

## How It Works

`keel gen` infers package configuration from the final Dockerfile stage:

- `LABEL NAME=...` -> package name, app name, namespace, and UDS host
- `EXPOSE` -> service port + UDS exposed port
- `ENV` -> container environment variables
- `USER` -> non-root security context
- `ENTRYPOINT` / `CMD` -> container command/args
- `HEALTHCHECK` -> liveness probe command

Compose mode (`--compose-file`) maps each compose service into a UDS/Zarf component:

- `services.*.build` -> image archive build input (`context`, `dockerfile`)
- `services.*.image` -> component image reference (used directly when `build` is not set)
- `services.*.ports` / `services.*.expose` -> service/deployment ports + UDS exposed port
- `services.*.environment` -> container environment variables
- `services.*.user` -> pod `runAsUser` / `runAsNonRoot` behavior
- `services.*.entrypoint` / `services.*.command` -> container command/args
- `services.*.healthcheck.test` -> liveness probe command

`keel gen` auto-detects source type from the input path (Dockerfile file path or compose YAML file path).
If a directory contains both a Dockerfile and a compose file, Keel errors as ambiguous and asks you to use `--dockerfile` or `--compose-file`.

## Notes

- Dockerfile mode builds use `docker buildx build` and output an OCI archive at `.dist/images/app.tar`.
- Compose mode builds one OCI archive per service that defines `build`, under `.dist/images/<service>.tar`.
- Docker + buildx is required (`docker buildx version`).
- `keel gen` always validates generated artifacts and overwrites `./.dist` by default.
- Build logs are quiet by default; use `--verbose` (or `--log-level debug`) for full build output.
