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
	configPath := filepath.Join(dir, "app-config.yml")
	secretPath := filepath.Join(dir, "db-password.txt")
	if err := os.WriteFile(configPath, []byte("key: value\n"), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	if err := os.WriteFile(secretPath, []byte("super-secret"), 0o644); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	app := model.ComposeAppSpec{
		Name:            "demo-stack",
		Namespace:       "demo-stack",
		Version:         "0.2.0",
		ComposeFilePath: "/workspace/examples/docker-compose.yml",
		Volumes: map[string]model.ComposeVolumeSpec{
			"app-data": {Name: "app-data"},
		},
		Secrets: map[string]model.ComposeSecretSpec{
			"db-password": {Name: "db-password", FilePath: secretPath},
		},
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
				Volumes: []model.ComposeVolumeMount{
					{Name: "app-data", Type: "volume", Target: "/var/lib/app"},
					{Type: "bind", SourcePath: configPath, Target: "/etc/app/config.yml", ReadOnly: true},
				},
				Secrets: []model.ComposeServiceSecretSpec{
					{Source: "db-password", Target: "db-password"},
				},
				DependsOn: []model.ComposeDependencySpec{
					{Service: "redis", Condition: "service_started"},
				},
				Resources: model.ComposeResourcesSpec{
					Limits:   model.ComposeResourceSet{CPU: "1", Memory: "512Mi"},
					Requests: model.ComposeResourceSet{CPU: "250m", Memory: "128Mi"},
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
		filepath.Join(dist.ManifestDir, "pvc-app-data.yaml"),
		filepath.Join(dist.ManifestDir, "secret-db-password.yaml"),
		filepath.Join(dist.ManifestDir, "configmap-api-bind-1.yaml"),
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
	if !strings.Contains(string(zarfConfig), "busybox:1.36") {
		t.Fatalf("expected dependency init-container image reference in zarf config")
	}
	for _, want := range []string{
		"variables:",
		"name: COMPOSE_SECRET_DB_PASSWORD",
		"sensitive: true",
		"prompt: true",
	} {
		if !strings.Contains(string(zarfConfig), want) {
			t.Fatalf("expected zarf config to contain %q", want)
		}
	}

	deployment, err := os.ReadFile(filepath.Join(dist.ManifestDir, "deployment-api.yaml"))
	if err != nil {
		t.Fatalf("read api deployment: %v", err)
	}
	for _, want := range []string{
		"initContainers:",
		"wait-redis",
		"busybox:1.36",
		"resources:",
		"cpu: \"1\"",
		"memory: \"512Mi\"",
		"mountPath: \"/etc/app/config.yml\"",
		"secretName: db-password",
	} {
		if !strings.Contains(string(deployment), want) {
			t.Fatalf("expected deployment to contain %q", want)
		}
	}

	secretManifest, err := os.ReadFile(filepath.Join(dist.ManifestDir, "secret-db-password.yaml"))
	if err != nil {
		t.Fatalf("read secret manifest: %v", err)
	}
	if strings.Contains(string(secretManifest), "super-secret") {
		t.Fatalf("expected secret manifest to avoid raw secret values")
	}
	if !strings.Contains(string(secretManifest), "###ZARF_VAR_COMPOSE_SECRET_DB_PASSWORD###") {
		t.Fatalf("expected secret manifest to contain templated zarf variable value")
	}
}
