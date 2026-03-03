package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/willswire/keel/internal/model"
	"github.com/willswire/keel/internal/validate"
)

func TestGenerateComposeAndValidate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dist := model.NewDistSpec(filepath.Join(dir, ".dist"))

	app := model.ComposeAppSpec{
		Name:            "demo-stack",
		Namespace:       "demo-stack",
		Version:         "0.2.0",
		ComposeFilePath: "/workspace/examples/docker-compose.yml",
		Services: []model.ComposeServiceSpec{
			{
				Name:      "api",
				Namespace: "demo-stack",
				Image:     "keel.local/api:latest",
				Build: &model.ComposeBuildSpec{
					ContextPath:    "/workspace/examples",
					DockerfilePath: "/workspace/examples/Dockerfile",
				},
				Dockerfile: model.DockerfileSpec{
					ExposedPorts: []model.Port{{Number: 8080, Protocol: "TCP"}},
					Env:          []model.EnvVar{{Name: "MESSAGE", Value: "hello"}},
					User:         "10001",
					Entrypoint:   `["python","/app/server.py"]`,
					Cmd:          `["--port","8080"]`,
				},
			},
			{
				Name:      "redis",
				Namespace: "demo-stack",
				Image:     "redis:7-alpine",
				Dockerfile: model.DockerfileSpec{
					ExposedPorts: []model.Port{{Number: 6379, Protocol: "TCP"}},
				},
			},
		},
	}

	if err := GenerateCompose(ComposeOptions{
		App:  app,
		Dist: dist,
	}); err != nil {
		t.Fatalf("GenerateCompose() error = %v", err)
	}

	if err := validate.ComposeDist(t.Context(), validate.ComposeOptions{
		Dist:                     dist,
		App:                      app,
		RequireBuiltImageArchive: false,
		ValidateWithZarf:         true,
	}); err != nil {
		t.Fatalf("validate.ComposeDist() error = %v", err)
	}

	for _, file := range []string{
		filepath.Join(dist.ManifestDir, "namespace.yaml"),
		filepath.Join(dist.ManifestDir, "deployment-api.yaml"),
		filepath.Join(dist.ManifestDir, "service-api.yaml"),
		filepath.Join(dist.ManifestDir, "uds-package-api.yaml"),
		filepath.Join(dist.ManifestDir, "deployment-redis.yaml"),
		filepath.Join(dist.ManifestDir, "service-redis.yaml"),
		filepath.Join(dist.ManifestDir, "uds-package-redis.yaml"),
		filepath.Join(dist.RootPath, "zarf.yaml"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("expected generated file %s: %v", file, err)
		}
	}

	zarfConfig, err := os.ReadFile(filepath.Join(dist.RootPath, "zarf.yaml"))
	if err != nil {
		t.Fatalf("read zarf config: %v", err)
	}
	if !strings.Contains(string(zarfConfig), "images/api.tar") {
		t.Fatalf("expected build service image archive reference in zarf config")
	}
	if !strings.Contains(string(zarfConfig), "redis:7-alpine") {
		t.Fatalf("expected image-only service reference in zarf config")
	}
}
