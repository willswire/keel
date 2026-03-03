package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newTestGenCommand() *cobra.Command {
	c := &cobra.Command{}
	c.Flags().String("containerfile", "./Containerfile", "")
	c.Flags().String("compose-file", "", "")
	c.Flags().String("context", ".", "")
	return c
}

func TestResolveComposePathFlagRelativeToInputFileDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputFile := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(inputFile, []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	c := newTestGenCommand()
	if err := c.Flags().Set("compose-file", "compose.dev.yml"); err != nil {
		t.Fatalf("set compose-file: %v", err)
	}

	path := resolveComposePath(c, genOptions{ComposeFile: "compose.dev.yml"}, inputFile)
	want := filepath.Join(dir, "compose.dev.yml")
	if path != want {
		t.Fatalf("unexpected compose path: got=%q want=%q", path, want)
	}
}

func TestResolveComposePathDefaultUsesCanonicalName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputFile := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(inputFile, []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	path := resolveComposePath(newTestGenCommand(), genOptions{}, inputFile)
	want := filepath.Join(dir, "compose.yaml")
	if path != want {
		t.Fatalf("unexpected compose path: got=%q want=%q", path, want)
	}
}

func TestResolveInputSourceFileContainerfile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputFile := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(inputFile, []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	source, detectedContainerfilePath, contextPath, composePath, err := resolveInputSource(newTestGenCommand(), genOptions{}, inputFile)
	if err != nil {
		t.Fatalf("resolveInputSource() error = %v", err)
	}
	if source != sourceContainerfile {
		t.Fatalf("expected containerfile source, got %q", source)
	}
	if detectedContainerfilePath != inputFile {
		t.Fatalf("unexpected containerfile path: got=%q want=%q", detectedContainerfilePath, inputFile)
	}
	if contextPath != dir {
		t.Fatalf("unexpected context path: got=%q want=%q", contextPath, dir)
	}
	if composePath != "" {
		t.Fatalf("did not expect compose path, got %q", composePath)
	}
}

func TestResolveInputSourceFileDockerfileCompatibility(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputFile := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(inputFile, []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	source, detectedContainerfilePath, contextPath, composePath, err := resolveInputSource(newTestGenCommand(), genOptions{}, inputFile)
	if err != nil {
		t.Fatalf("resolveInputSource() error = %v", err)
	}
	if source != sourceContainerfile {
		t.Fatalf("expected containerfile source, got %q", source)
	}
	if detectedContainerfilePath != inputFile {
		t.Fatalf("unexpected containerfile path: got=%q want=%q", detectedContainerfilePath, inputFile)
	}
	if contextPath != dir {
		t.Fatalf("unexpected context path: got=%q want=%q", contextPath, dir)
	}
	if composePath != "" {
		t.Fatalf("did not expect compose path, got %q", composePath)
	}
}

func TestResolveInputSourceFileCompose(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputFile := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(inputFile, []byte("services:\n  app:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	source, detectedContainerfilePath, contextPath, composePath, err := resolveInputSource(newTestGenCommand(), genOptions{}, inputFile)
	if err != nil {
		t.Fatalf("resolveInputSource() error = %v", err)
	}
	if source != sourceCompose {
		t.Fatalf("expected compose source, got %q", source)
	}
	if composePath != inputFile {
		t.Fatalf("unexpected compose path: got=%q want=%q", composePath, inputFile)
	}
	if detectedContainerfilePath != "" || contextPath != "" {
		t.Fatalf("did not expect containerfile/context paths: containerfile=%q context=%q", detectedContainerfilePath, contextPath)
	}
}

func TestResolveInputSourceDirectoryComposeOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	source, _, _, detectedComposePath, err := resolveInputSource(newTestGenCommand(), genOptions{}, dir)
	if err != nil {
		t.Fatalf("resolveInputSource() error = %v", err)
	}
	if source != sourceCompose {
		t.Fatalf("expected compose source, got %q", source)
	}
	if detectedComposePath != composePath {
		t.Fatalf("unexpected compose path: got=%q want=%q", detectedComposePath, composePath)
	}
}

func TestResolveInputSourceDirectoryContainerfileOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	containerfilePath := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(containerfilePath, []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	source, detectedContainerfilePath, contextPath, composePath, err := resolveInputSource(newTestGenCommand(), genOptions{}, dir)
	if err != nil {
		t.Fatalf("resolveInputSource() error = %v", err)
	}
	if source != sourceContainerfile {
		t.Fatalf("expected containerfile source, got %q", source)
	}
	if detectedContainerfilePath != containerfilePath {
		t.Fatalf("unexpected containerfile path: got=%q want=%q", detectedContainerfilePath, containerfilePath)
	}
	if contextPath != dir {
		t.Fatalf("unexpected context path: got=%q want=%q", contextPath, dir)
	}
	if composePath != "" {
		t.Fatalf("did not expect compose path, got %q", composePath)
	}
}

func TestResolveInputSourceDirectoryDockerfileOnlyCompatibility(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	source, detectedContainerfilePath, contextPath, composePath, err := resolveInputSource(newTestGenCommand(), genOptions{}, dir)
	if err != nil {
		t.Fatalf("resolveInputSource() error = %v", err)
	}
	if source != sourceContainerfile {
		t.Fatalf("expected containerfile source, got %q", source)
	}
	if detectedContainerfilePath != dockerfilePath {
		t.Fatalf("unexpected containerfile path: got=%q want=%q", detectedContainerfilePath, dockerfilePath)
	}
	if contextPath != dir {
		t.Fatalf("unexpected context path: got=%q want=%q", contextPath, dir)
	}
	if composePath != "" {
		t.Fatalf("did not expect compose path, got %q", composePath)
	}
}

func TestResolveInputSourceDirectoryDockerfileAndContainerfileAmbiguous(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	_, _, _, _, err := resolveInputSource(newTestGenCommand(), genOptions{}, dir)
	if err == nil {
		t.Fatal("expected ambiguity error when both Dockerfile and Containerfile exist")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("expected ambiguity error message, got: %v", err)
	}
}

func TestResolveInputSourceDirectoryAmbiguous(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  app:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	_, _, _, _, err := resolveInputSource(newTestGenCommand(), genOptions{}, dir)
	if err == nil {
		t.Fatal("expected ambiguity error when both Containerfile and compose file exist")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("expected ambiguity error message, got: %v", err)
	}
}

func TestDetectComposePathPrefersCanonicalComposeFilename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	composePath := filepath.Join(dir, "compose.yaml")
	legacyPath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(legacyPath, []byte("services:\n  app:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatalf("write compose.yaml: %v", err)
	}

	detected := detectComposePath(dir)
	if detected != composePath {
		t.Fatalf("unexpected compose path: got=%q want=%q", detected, composePath)
	}
}
