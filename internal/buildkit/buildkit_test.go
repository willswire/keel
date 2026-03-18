package build

import (
	"strings"
	"testing"
)

func TestBuildxArgs(t *testing.T) {
	args := buildxArgs(
		"",
		[]string{"linux/amd64", "linux/arm64"},
		"/tmp/example/Dockerfile",
		"ghcr.io/willswire/hello-world-example:dev",
		"/tmp/.dist/images/app.tar",
		"/tmp/example",
		"",
	)

	want := []string{
		"buildx", "build",
		"--platform", "linux/amd64,linux/arm64",
		"--file", "/tmp/example/Dockerfile",
		"--tag", "ghcr.io/willswire/hello-world-example:dev",
		"--output", "type=oci,name=ghcr.io/willswire/hello-world-example:dev,dest=/tmp/.dist/images/app.tar",
		"/tmp/example",
	}

	if len(args) != len(want) {
		t.Fatalf("unexpected argument count: got=%d want=%d\nargs=%q", len(args), len(want), args)
	}

	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("unexpected arg at index %d: got=%q want=%q\nargs=%q", i, args[i], want[i], args)
		}
	}
}

func TestBuildxArgsOutputSpecContainsImageName(t *testing.T) {
	args := buildxArgs(
		"",
		[]string{"linux/amd64"},
		"/workspace/example/Dockerfile",
		"ghcr.io/willswire/app:dev",
		"/workspace/.dist/images/app.tar",
		"/workspace/example",
		"",
	)

	var outputSpec string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--output" {
			outputSpec = args[i+1]
			break
		}
	}
	if outputSpec == "" {
		t.Fatal("missing --output argument")
	}
	if !strings.Contains(outputSpec, "type=oci") {
		t.Fatalf("output spec missing type=oci: %q", outputSpec)
	}
	if !strings.Contains(outputSpec, "name=ghcr.io/willswire/app:dev") {
		t.Fatalf("output spec missing image name: %q", outputSpec)
	}
	if !strings.Contains(outputSpec, "dest=/workspace/.dist/images/app.tar") {
		t.Fatalf("output spec missing destination archive: %q", outputSpec)
	}
}

func TestBuildxArgsIncludesBuilderWhenProvided(t *testing.T) {
	args := buildxArgs(
		"keel-1234",
		[]string{"linux/amd64"},
		"/workspace/example/Dockerfile",
		"ghcr.io/willswire/app:dev",
		"/workspace/.dist/images/app.tar",
		"/workspace/example",
		"",
	)
	if len(args) < 4 {
		t.Fatalf("unexpected args: %q", args)
	}
	if args[2] != "--builder" || args[3] != "keel-1234" {
		t.Fatalf("missing builder args: %q", args)
	}
}

func TestNeedsContainerBuilderFallback(t *testing.T) {
	if !needsContainerBuilderFallback("ERROR: OCI exporter is not supported for the docker driver") {
		t.Fatal("expected fallback detection for docker driver OCI exporter error")
	}
	if needsContainerBuilderFallback("some unrelated docker error") {
		t.Fatal("did not expect fallback detection for unrelated error")
	}
}

func TestParseBuildxDriver(t *testing.T) {
	const inspect = `Name: default
Driver: docker
Nodes:
Name: default0
`
	got := parseBuildxDriver(inspect)
	if got != "docker" {
		t.Fatalf("unexpected driver: got=%q want=%q", got, "docker")
	}
}

func TestParseBuildxDriverMissing(t *testing.T) {
	const inspect = `Name: default
Nodes:
Name: default0
`
	got := parseBuildxDriver(inspect)
	if got != "" {
		t.Fatalf("unexpected driver: got=%q want empty", got)
	}
}
