package compose

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/willswire/keel/internal/model"
)

func TestParseFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}
	content := `name: demo_stack
services:
  web:
    build:
      context: .
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

	web, ok := serviceByName(spec.Services, "web")
	if !ok {
		t.Fatalf("expected web service")
	}
	if web.Image != "keel.local/web:latest" {
		t.Fatalf("expected default image for build-only service, got %q", web.Image)
	}
	if web.Build == nil {
		t.Fatalf("expected build spec for web service")
	}
	if web.Build.ContainerfilePath != filepath.Join(dir, "Containerfile") {
		t.Fatalf("unexpected containerfile path: %q", web.Build.ContainerfilePath)
	}
	if got, want := web.Container.Healthcheck, `CMD ["python","/app/server.py","--healthcheck"]`; got != want {
		t.Fatalf("unexpected healthcheck: got %q want %q", got, want)
	}
	if got, want := len(web.Container.ExposedPorts), 1; got != want {
		t.Fatalf("expected %d exposed port for web, got %d", want, got)
	}

	redis, ok := serviceByName(spec.Services, "redis")
	if !ok {
		t.Fatalf("expected redis service")
	}
	if redis.Image != "redis:7-alpine" {
		t.Fatalf("unexpected redis image: %q", redis.Image)
	}
	if redis.Build != nil {
		t.Fatalf("did not expect build spec for redis")
	}
	if got, want := len(redis.Container.ExposedPorts), 1; got != want {
		t.Fatalf("expected %d exposed port for redis, got %d", want, got)
	}
	if redis.Container.ExposedPorts[0].Number != 6379 {
		t.Fatalf("unexpected redis port: %d", redis.Container.ExposedPorts[0].Number)
	}
}

func TestParseFileMissingImageAndBuild(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
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

func TestParseFileBuildDefaultsToContainerfileWhenDefaultBuildFileMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	path := filepath.Join(dir, "compose.yaml")
	content := `services:
  app:
    build:
      context: .
    ports:
      - "8080:8080"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	spec, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}

	app, ok := serviceByName(spec.Services, "app")
	if !ok {
		t.Fatalf("expected app service")
	}
	if app.Build == nil {
		t.Fatalf("expected build spec for app service")
	}
	if got, want := app.Build.ContainerfilePath, filepath.Join(dir, "Containerfile"); got != want {
		t.Fatalf("unexpected build file path: got=%q want=%q", got, want)
	}
}

func TestParseExampleComposeFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "examples", "compose.yaml")
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

func TestParseFileProfiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	content := `services:
  api:
    image: nginx:1.27
  worker:
    image: busybox:1.36
    profiles: ["jobs"]
    depends_on:
      - api
  debug:
    image: busybox:1.36
    profiles: ["debug"]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	defaultSpec, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if got, want := sortedServiceNames(defaultSpec.Services), []string{"api"}; !equalStrings(got, want) {
		t.Fatalf("unexpected default services: got=%v want=%v", got, want)
	}

	jobsSpec, err := ParseFileWithOptions(path, ParseOptions{Profiles: []string{"jobs"}})
	if err != nil {
		t.Fatalf("ParseFileWithOptions() error = %v", err)
	}
	if got, want := sortedServiceNames(jobsSpec.Services), []string{"api", "worker"}; !equalStrings(got, want) {
		t.Fatalf("unexpected jobs services: got=%v want=%v", got, want)
	}
}

func TestParseFileDefaultProjectNameFromFileName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "wordpress-mysql.compose.yml")
	content := `services:
  app:
    image: nginx:1.27
    ports:
      - "8080:8080"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	spec, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if spec.Name != "wordpress-mysql" {
		t.Fatalf("expected project name from file name, got %q", spec.Name)
	}
}

func TestParseFileEnvFileAndEnvironmentOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte("FOO=from-file\nBAR=old\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	path := filepath.Join(dir, "compose.yaml")
	content := `services:
  app:
    image: nginx:1.27
    env_file:
      - app.env
      - path: missing.env
        required: false
    environment:
      BAR: from-inline
      BAZ: "42"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	spec, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}

	app, ok := serviceByName(spec.Services, "app")
	if !ok {
		t.Fatalf("expected app service")
	}
	if got, want := envValue(app.Container.Env, "FOO"), "from-file"; got != want {
		t.Fatalf("unexpected FOO value: got=%q want=%q", got, want)
	}
	if got, want := envValue(app.Container.Env, "BAR"), "from-inline"; got != want {
		t.Fatalf("unexpected BAR value: got=%q want=%q", got, want)
	}
	if got, want := envValue(app.Container.Env, "BAZ"), "42"; got != want {
		t.Fatalf("unexpected BAZ value: got=%q want=%q", got, want)
	}
}

func TestParseFileVolumesSecretsIncludeExtensions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app-secret.txt"), []byte("super-secret"), 0o644); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.conf"), []byte("listen=8080\n"), 0o644); err != nil {
		t.Fatalf("write bind file: %v", err)
	}

	basePath := filepath.Join(dir, "base.compose.yml")
	base := `x-common: &common
  environment:
    SHARED: "yes"

services:
  db:
    image: postgres:16-alpine
    <<: *common
`
	if err := os.WriteFile(basePath, []byte(base), 0o644); err != nil {
		t.Fatalf("write base compose: %v", err)
	}

	path := filepath.Join(dir, "compose.yaml")
	content := `include:
  - base.compose.yml

services:
  app:
    image: nginx:1.27
    depends_on:
      db:
        condition: service_started
    volumes:
      - app_data:/var/lib/app
      - ./app.conf:/etc/app/app.conf:ro
    secrets:
      - source: app_secret
        target: app-secret
    develop:
      watch:
        - action: sync
          path: ./
          target: /workspace

volumes:
  app_data: {}

secrets:
  app_secret:
    file: ./app-secret.txt
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	spec, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}

	if got, want := sortedServiceNames(spec.Services), []string{"app", "db"}; !equalStrings(got, want) {
		t.Fatalf("unexpected services: got=%v want=%v", got, want)
	}
	app, ok := serviceByName(spec.Services, "app")
	if !ok {
		t.Fatalf("expected app service")
	}
	if got, want := len(app.Volumes), 2; got != want {
		t.Fatalf("unexpected volume count: got=%d want=%d", got, want)
	}
	if app.Volumes[0].Type != "volume" || app.Volumes[0].Name != "app-data" {
		t.Fatalf("unexpected named volume: %#v", app.Volumes[0])
	}
	if app.Volumes[1].Type != "bind" {
		t.Fatalf("expected bind volume, got %#v", app.Volumes[1])
	}
	if got := filepath.Base(app.Volumes[1].SourcePath); got != "app.conf" {
		t.Fatalf("unexpected bind source path: %q", app.Volumes[1].SourcePath)
	}
	if got, want := len(app.Secrets), 1; got != want {
		t.Fatalf("unexpected secret refs: got=%d want=%d", got, want)
	}
	if got, want := app.Secrets[0].Source, "app-secret"; got != want {
		t.Fatalf("unexpected normalized secret source: got=%q want=%q", got, want)
	}
	if _, ok := spec.Secrets["app-secret"]; !ok {
		t.Fatalf("expected top-level normalized secret app-secret")
	}
}

func TestParseGoldenSamples(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		file         string
		serviceCount int
	}{
		{name: "wordpress mysql", file: filepath.Join("testdata", "golden", "wordpress-mysql.compose.yml"), serviceCount: 2},
		{name: "prometheus grafana", file: filepath.Join("testdata", "golden", "prometheus-grafana.compose.yml"), serviceCount: 2},
		{name: "flask redis", file: filepath.Join("testdata", "golden", "flask-redis.compose.yml"), serviceCount: 2},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, err := ParseFile(tc.file)
			if err != nil {
				t.Fatalf("ParseFile() error = %v", err)
			}
			if got, want := len(spec.Services), tc.serviceCount; got != want {
				t.Fatalf("unexpected service count: got=%d want=%d", got, want)
			}
		})
	}
}

func serviceByName(services []model.ComposeServiceSpec, name string) (model.ComposeServiceSpec, bool) {
	for _, svc := range services {
		if svc.Name == name {
			return svc, true
		}
	}
	return model.ComposeServiceSpec{}, false
}

func sortedServiceNames(services []model.ComposeServiceSpec) []string {
	names := make([]string, 0, len(services))
	for _, svc := range services {
		names = append(names, svc.Name)
	}
	sort.Strings(names)
	return names
}

func envValue(env []model.EnvVar, name string) string {
	for _, item := range env {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}

func equalStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
