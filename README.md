# Keel

`keel` turns your single containerized app into a UDS package.

If you already know how to build containers, `keel` helps you ship to Kubernetes without writing Kubernetes YAML.

It reads Dockerfile instructions and generates a deployable `.dist/` package for Zarf/UDS.

## Commands

- `keel gen [PATH]`: Parse Dockerfile, render manifests + `zarf.yaml`, build OCI image archive, and validate output.
- `keel version`: Print the CLI version.

## Quickstart

```bash
# Build the Keel binary
go build -o ./build/keel .

# Generate the Hello World Zarf package with Keel
./build/keel gen example

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

## Notes

- Builds use `docker buildx build` and output an OCI archive at `.dist/images/app.tar`.
- Docker + buildx is required (`docker buildx version`).
- `keel gen` always validates generated artifacts and overwrites `./.dist` by default.
- Build logs are quiet by default; use `--verbose` (or `--log-level debug`) for full build output.
