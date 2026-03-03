package containerfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFileFinalStage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "Containerfile")
	content := `FROM busybox:1.0 AS builder
EXPOSE 1234
USER 1000

FROM nginx:latest
LABEL NAME=hello-world
ENV A=1 B=2
ENV A=3
ENV TAGLINE="hello from keel"
EXPOSE 8080/tcp 8443
USER 2000:2000
CMD ["nginx", "-g", "daemon off;"]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	spec, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}

	if spec.User != "2000:2000" {
		t.Fatalf("expected USER from final stage, got %q", spec.User)
	}
	if spec.Name != "hello-world" {
		t.Fatalf("expected NAME label from final stage, got %q", spec.Name)
	}
	if got, want := len(spec.ExposedPorts), 2; got != want {
		t.Fatalf("expected %d exposed ports, got %d", want, got)
	}
	if spec.ExposedPorts[0].Number != 8080 {
		t.Fatalf("expected first exposed port 8080, got %d", spec.ExposedPorts[0].Number)
	}
	if got, want := len(spec.Env), 3; got != want {
		t.Fatalf("expected %d env vars, got %d", want, got)
	}
	if spec.Env[0].Name != "A" || spec.Env[0].Value != "3" {
		t.Fatalf("expected ENV override for A=3, got %#v", spec.Env[0])
	}
	if spec.Env[2].Name != "TAGLINE" || spec.Env[2].Value != "hello from keel" {
		t.Fatalf("expected TAGLINE to preserve spaces, got %#v", spec.Env[2])
	}
}

func TestParseFileMissingExpose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "Containerfile")
	content := `FROM alpine
LABEL NAME=example
USER 1000
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	if _, err := ParseFile(path); err == nil {
		t.Fatalf("expected error for missing EXPOSE")
	}
}

func TestParseFileMissingUser(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "Containerfile")
	content := `FROM alpine
LABEL NAME=example
EXPOSE 8080
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	if _, err := ParseFile(path); err == nil {
		t.Fatalf("expected error for missing USER")
	}
}

func TestParseFileMissingNameLabel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "Containerfile")
	content := `FROM alpine
EXPOSE 8080
USER 1000
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	if _, err := ParseFile(path); err == nil {
		t.Fatalf("expected error for missing LABEL NAME")
	}
}
