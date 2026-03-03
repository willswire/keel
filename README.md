# Keel

A tool for turning containerized applications into deployable UDS packages, whether the app is a single container or a multi-service stack.

`keel` accepts either a `Containerfile` or a [Compose Specification](https://compose-spec.io/) file and translates that input into Kubernetes resources tailored for running on UDS.

Like Kompose, Keel is a convenience tool for moving from local container environments to cluster deployment. Transformations from Compose to Kubernetes/UDS are practical and high-signal, but not always a byte-for-byte representation of runtime behavior.

## Commands

- `keel gen [PATH]`: Parse Containerfile (default) or Compose YAML, render manifests + `zarf.yaml`, build OCI image archive(s), and validate output.
- `keel version`: Print the CLI version.

## Quickstart

```bash
# Build the Keel binary
go build -o ./build/keel .

# Generate from Containerfile
./build/keel gen examples/Containerfile

# Generate from Compose
./build/keel gen examples/compose.yaml

# Package with Zarf
zarf package create .dist

# (Optionally) Deploy k3d core slim dev and the package
uds deploy k3d-core-slim-dev:latest
zarf package deploy --confirm zarf-package-hello-world-*-0.1.0.tar.zst
```

## How It Works

For single-container apps, `keel gen` infers package configuration from the final container build stage:

- `LABEL NAME=...` -> package name, app name, namespace, and UDS host
- `EXPOSE` -> service port + UDS exposed port
- `ENV` -> container environment variables
- `USER` -> non-root security context
- `ENTRYPOINT` / `CMD` -> container command/args
- `HEALTHCHECK` -> liveness probe command

Compose mode (`--compose-file`) maps each compose service into a UDS/Zarf component:

- `services.*.build` -> image archive build input (`context`, optional build-file selector)
- `services.*.image` -> component image reference (used directly when `build` is not set)
- `services.*.ports` / `services.*.expose` -> service/deployment ports + UDS exposed port
- `services.*.env_file` + `services.*.environment` -> merged container env (inline env overrides env_file)
- `services.*.user` -> pod `runAsUser` / `runAsNonRoot` behavior
- `services.*.entrypoint` / `services.*.command` -> container command/args
- `services.*.healthcheck.test` -> liveness probe command
- `services.*.volumes` + top-level `volumes` -> PVC mounts and bind-mounted config files/directories (ConfigMap when possible)
- `services.*.secrets` + top-level `secrets` -> Kubernetes Secret mounts and generated Secret manifests with `###ZARF_VAR_*###` placeholders (plus generated `variables` entries in `zarf.yaml`)
- `services.*.depends_on` -> init-container wait logic for known dependent service ports
- `services.*.deploy.resources` -> container resources requests/limits
- `services.*.profiles` -> profile-based service selection (activate with `--compose-profile`)
- top-level `include` -> local compose-file include merge

Keel tolerates compose extensions and dev-only keys without failing generation:

- top-level/service `x-*` extension blocks (including YAML anchors/aliases/merge keys)
- `services.*.develop` keys (ignored during manifest generation)

`keel gen` auto-detects source type from the input path (Containerfile path or compose YAML path).
When scanning a directory, Keel prefers canonical compose filenames (`compose.yaml`, `compose.yml`) before legacy names (`docker-compose.yaml`, `docker-compose.yml`).
If a directory contains both a build file and a compose file, Keel errors as ambiguous and asks you to use `--containerfile` or `--compose-file`.
If a directory contains both `Dockerfile` and `Containerfile`, Keel errors as ambiguous and asks you to use `--containerfile`.

## Notes

- Containerfile mode builds use `docker buildx build` and output an OCI archive at `.dist/images/app.tar`.
- Compose mode builds one OCI archive per service that defines `build`, under `.dist/images/<service>.tar`.
- If all compose services are profile-gated, use `--compose-profile <name>` (repeatable) to select services.
- `docker buildx` is required (`docker buildx version`).
- `keel gen` always validates generated artifacts and overwrites `./.dist` by default.
- Build logs are quiet by default; use `--verbose` (or `--log-level debug`) for full build output.
