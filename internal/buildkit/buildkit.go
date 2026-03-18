package build

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Options struct {
	Containerfile string
	Context       string
	Image         string
	Platforms     []string
	OutputArchive string
	Target        string
	VerboseBuild  bool
}

const ociExporterUnsupportedMsg = "OCI exporter is not supported for the docker driver"

func ImageArchive(ctx context.Context, opts Options) error {
	if strings.TrimSpace(opts.Containerfile) == "" {
		return fmt.Errorf("containerfile path is required")
	}
	if strings.TrimSpace(opts.Context) == "" {
		return fmt.Errorf("build context path is required")
	}
	if strings.TrimSpace(opts.Image) == "" {
		return fmt.Errorf("image reference is required")
	}
	if len(opts.Platforms) == 0 {
		return fmt.Errorf("at least one platform is required")
	}

	outputArchiveAbs, err := filepath.Abs(opts.OutputArchive)
	if err != nil {
		return fmt.Errorf("resolve output archive path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputArchiveAbs), 0o755); err != nil {
		return fmt.Errorf("create image output directory: %w", err)
	}
	if err := os.Remove(outputArchiveAbs); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing image archive %s: %w", outputArchiveAbs, err)
	}

	containerfileAbs, err := filepath.Abs(opts.Containerfile)
	if err != nil {
		return fmt.Errorf("resolve containerfile path: %w", err)
	}
	contextAbs, err := filepath.Abs(opts.Context)
	if err != nil {
		return fmt.Errorf("resolve context path: %w", err)
	}

	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker executable %q not found in PATH", "docker")
	}
	if err := ensureBuildxAvailable(ctx, dockerBin); err != nil {
		return err
	}

	builder := ""
	driver, err := currentBuildxDriver(ctx, dockerBin)
	if err == nil && driver == "docker" {
		var cleanup func()
		builder, cleanup, err = createContainerBuilder(ctx, dockerBin)
		if err != nil {
			return fmt.Errorf("create docker-container builder for OCI export: %w", err)
		}
		defer cleanup()
	}

	args := buildxArgs(builder, opts.Platforms, containerfileAbs, opts.Image, outputArchiveAbs, contextAbs, opts.Target)
	output, err := runDockerBuildx(ctx, dockerBin, args, opts.VerboseBuild)
	if err == nil {
		return nil
	}

	// Fallback safety net if driver detection failed or driver changed at runtime.
	if builder != "" || !needsContainerBuilderFallback(output) {
		return fmt.Errorf("docker buildx build failed: %w%s", err, formatCommandOutputHint(output))
	}

	fallbackBuilder, cleanup, createErr := createContainerBuilder(ctx, dockerBin)
	if createErr != nil {
		return fmt.Errorf("docker buildx build failed and fallback builder setup failed: %w", createErr)
	}
	defer cleanup()

	fallbackArgs := buildxArgs(fallbackBuilder, opts.Platforms, containerfileAbs, opts.Image, outputArchiveAbs, contextAbs, opts.Target)
	fallbackOutput, err := runDockerBuildx(ctx, dockerBin, fallbackArgs, opts.VerboseBuild)
	if err != nil {
		return fmt.Errorf("docker buildx build failed using fallback builder %s: %w%s", fallbackBuilder, err, formatCommandOutputHint(fallbackOutput))
	}

	return nil
}

func ensureBuildxAvailable(ctx context.Context, dockerBin string) error {
	cmd := exec.CommandContext(ctx, dockerBin, "buildx", "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker buildx is not available; install or enable the buildx plugin: %w", err)
	}
	return nil
}

func runDockerBuildx(ctx context.Context, dockerBin string, args []string, verbose bool) (string, error) {
	cmd := exec.CommandContext(ctx, dockerBin, args...)
	var output bytes.Buffer
	if verbose {
		cmd.Stdout = io.MultiWriter(os.Stdout, &output)
		cmd.Stderr = io.MultiWriter(os.Stderr, &output)
	} else {
		cmd.Stdout = &output
		cmd.Stderr = &output
	}
	err := cmd.Run()
	return output.String(), err
}

func formatCommandOutputHint(output string) string {
	const maxChars = 1200
	s := strings.TrimSpace(output)
	if s == "" {
		return ""
	}
	if len(s) > maxChars {
		s = s[:maxChars] + "...(truncated)"
	}
	return "\n\nbuild output:\n" + s
}

func currentBuildxDriver(ctx context.Context, dockerBin string) (string, error) {
	cmd := exec.CommandContext(ctx, dockerBin, "buildx", "inspect")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect current buildx builder: %w", err)
	}
	driver := parseBuildxDriver(string(out))
	if driver == "" {
		return "", fmt.Errorf("could not determine buildx driver from inspect output")
	}
	return driver, nil
}

func parseBuildxDriver(inspectOutput string) string {
	for _, line := range strings.Split(inspectOutput, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "driver:") {
			continue
		}
		driver := strings.TrimSpace(strings.TrimPrefix(lower, "driver:"))
		return driver
	}
	return ""
}

func needsContainerBuilderFallback(output string) bool {
	return strings.Contains(strings.ToLower(output), strings.ToLower(ociExporterUnsupportedMsg))
}

func createContainerBuilder(ctx context.Context, dockerBin string) (string, func(), error) {
	builder := fmt.Sprintf("keel-%d-%d", os.Getpid(), time.Now().UnixNano())

	createCmd := exec.CommandContext(ctx, dockerBin, "buildx", "create", "--name", builder, "--driver", "docker-container")
	if out, err := createCmd.CombinedOutput(); err != nil {
		return "", func() {}, fmt.Errorf("create docker buildx builder: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	inspectCmd := exec.CommandContext(ctx, dockerBin, "buildx", "inspect", "--bootstrap", builder)
	if out, err := inspectCmd.CombinedOutput(); err != nil {
		return "", func() {
			cleanupContainerBuilder(dockerBin, builder)
		}, fmt.Errorf("bootstrap docker buildx builder: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	cleanup := func() {
		cleanupContainerBuilder(dockerBin, builder)
	}
	return builder, cleanup, nil
}

func cleanupContainerBuilder(dockerBin, builder string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rmCmd := exec.CommandContext(ctx, dockerBin, "buildx", "rm", "--force", builder)
	_ = rmCmd.Run()
}

func buildxArgs(builder string, platforms []string, containerfileAbs, image, outputArchiveAbs, contextAbs, target string) []string {
	outputSpec := fmt.Sprintf("type=oci,name=%s,dest=%s", image, outputArchiveAbs)
	args := []string{
		"buildx", "build",
	}
	if strings.TrimSpace(builder) != "" {
		args = append(args, "--builder", builder)
	}
	if strings.TrimSpace(target) != "" {
		args = append(args, "--target", target)
	}
	args = append(args,
		"--platform", strings.Join(platforms, ","),
		"--file", containerfileAbs,
		"--tag", image,
		"--output", outputSpec,
		contextAbs,
	)
	return args
}
