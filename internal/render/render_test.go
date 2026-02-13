package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/willswire/keel/internal/model"
	"github.com/willswire/keel/internal/validate"
)

func TestGenerateAndValidate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dist := model.NewDistSpec(filepath.Join(dir, ".dist"))

	app := model.AppSpec{
		Name:      "hello-world",
		Namespace: "hello-world",
		Image:     "registry.example/hello-world:dev",
		Version:   "0.1.0",
		Platforms: []string{
			"linux/amd64",
		},
		Dockerfile: model.DockerfileSpec{
			ExposedPorts: []model.Port{{Number: 3000, Protocol: "TCP"}},
			Name:         "hello-world",
			User:         "1000",
			Entrypoint:   `["python","/app/server.py"]`,
			Cmd:          `["--port","3000"]`,
			Healthcheck:  `--interval=30s CMD ["python","/app/server.py","--healthcheck"]`,
			Env: []model.EnvVar{
				{Name: "FOO", Value: "bar"},
			},
		},
	}

	if err := Generate(Options{
		App:            app,
		Dist:           dist,
		ZarfMinVersion: "v0.67.0",
	}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if err := validate.Dist(t.Context(), validate.Options{
		Dist:                dist,
		RequireImageArchive: false,
		ValidateWithZarf:    true,
	}); err != nil {
		t.Fatalf("validate.Dist() error = %v", err)
	}

	for _, file := range []string{
		filepath.Join(dist.ManifestDir, "namespace.yaml"),
		filepath.Join(dist.ManifestDir, "deployment.yaml"),
		filepath.Join(dist.ManifestDir, "service.yaml"),
		filepath.Join(dist.ManifestDir, "uds-package.yaml"),
		filepath.Join(dist.RootPath, "zarf.yaml"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("expected generated file %s: %v", file, err)
		}
	}

	deploymentPath := filepath.Join(dist.ManifestDir, "deployment.yaml")
	deployment, err := os.ReadFile(deploymentPath)
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	for _, want := range []string{
		`command:`,
		`- "python"`,
		`- "/app/server.py"`,
		`args:`,
		`- "--port"`,
		`- "3000"`,
		`livenessProbe:`,
		`- "--healthcheck"`,
	} {
		if !strings.Contains(string(deployment), want) {
			t.Fatalf("expected deployment to contain %q", want)
		}
	}
}
