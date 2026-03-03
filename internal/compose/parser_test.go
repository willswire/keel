package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	content := `name: demo_stack
services:
  web:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
    environment:
      MESSAGE: hello
      APP_PORT: "8080"
    user: "10001"
    command: ["--port", "8080"]
    healthcheck:
      test: ["CMD", "python", "/app/server.py", "--healthcheck"]
  redis:
    image: redis:7-alpine
    ports:
      - target: 6379
        protocol: tcp
    environment:
      - ALLOW_EMPTY_PASSWORD=yes
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	spec, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}

	if spec.Name != "demo-stack" {
		t.Fatalf("expected normalized project name demo-stack, got %q", spec.Name)
	}
	if got, want := len(spec.Services), 2; got != want {
		t.Fatalf("expected %d services, got %d", want, got)
	}

	var webSvcFound bool
	var redisSvcFound bool
	for _, svc := range spec.Services {
		switch svc.Name {
		case "web":
			webSvcFound = true
			if svc.Image != "keel.local/web:latest" {
				t.Fatalf("expected default image for build-only service, got %q", svc.Image)
			}
			if svc.Build == nil {
				t.Fatalf("expected build spec for web service")
			}
			if svc.Build.DockerfilePath != filepath.Join(dir, "Dockerfile") {
				t.Fatalf("unexpected dockerfile path: %q", svc.Build.DockerfilePath)
			}
			if got, want := svc.Dockerfile.Healthcheck, `CMD ["python","/app/server.py","--healthcheck"]`; got != want {
				t.Fatalf("unexpected healthcheck: got %q want %q", got, want)
			}
			if got, want := len(svc.Dockerfile.ExposedPorts), 1; got != want {
				t.Fatalf("expected %d exposed port for web, got %d", want, got)
			}
		case "redis":
			redisSvcFound = true
			if svc.Image != "redis:7-alpine" {
				t.Fatalf("unexpected redis image: %q", svc.Image)
			}
			if svc.Build != nil {
				t.Fatalf("did not expect build spec for redis")
			}
			if got, want := len(svc.Dockerfile.ExposedPorts), 1; got != want {
				t.Fatalf("expected %d exposed port for redis, got %d", want, got)
			}
			if svc.Dockerfile.ExposedPorts[0].Number != 6379 {
				t.Fatalf("unexpected redis port: %d", svc.Dockerfile.ExposedPorts[0].Number)
			}
		}
	}

	if !webSvcFound || !redisSvcFound {
		t.Fatalf("expected web and redis services, got %#v", spec.Services)
	}
}

func TestParseFileMissingImageAndBuild(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	content := `services:
  app:
    ports: ["8080:8080"]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	if _, err := ParseFile(path); err == nil {
		t.Fatal("expected error when service is missing image and build")
	}
}

func TestParseExampleComposeFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "examples", "docker-compose.yml")
	spec, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() example error = %v", err)
	}
	if spec.Name != "hello-stack" {
		t.Fatalf("unexpected compose project name: %q", spec.Name)
	}
	if got, want := len(spec.Services), 2; got != want {
		t.Fatalf("expected %d services, got %d", want, got)
	}
}
