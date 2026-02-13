package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	zlogger "github.com/zarf-dev/zarf/src/pkg/logger"

	build "github.com/willswire/keel/internal/buildkit"
	"github.com/willswire/keel/internal/dockerfile"
	"github.com/willswire/keel/internal/model"
	"github.com/willswire/keel/internal/render"
	"github.com/willswire/keel/internal/validate"
)

var defaultPlatforms = []string{"linux/amd64", "linux/arm64"}

type genOptions struct {
	Dockerfile         string
	Context            string
	Version            string
	Output             string
	ZarfMinVersion     string
	SkipImageBuild     bool
	SkipZarfValidation bool
}

func newGenCmd() *cobra.Command {
	opts := &genOptions{}

	cmd := &cobra.Command{
		Use:     "gen [PATH]",
		Aliases: []string{"dist"},
		Short:   "Generate a .dist package layout from a Dockerfile",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inputPath := "."
			if len(args) == 1 {
				inputPath = filepath.Clean(args[0])
			}
			return runGen(cmd.Context(), cmd, *opts, inputPath)
		},
	}

	cmd.Flags().StringVar(&opts.Dockerfile, "dockerfile", "./Dockerfile", "Path to Dockerfile")
	cmd.Flags().StringVar(&opts.Context, "context", ".", "Docker build context")
	cmd.Flags().StringVar(&opts.Version, "version", model.DefaultVersion, "Package version written to zarf.yaml")
	cmd.Flags().StringVar(&opts.Output, "output", ".dist", "Output directory")
	cmd.Flags().StringVar(&opts.ZarfMinVersion, "zarf-min-version", "v0.67.0", "Documented minimum Zarf version for image archive support")
	cmd.Flags().BoolVar(&opts.SkipImageBuild, "skip-image-build", false, "Skip docker buildx image archive build")
	cmd.Flags().BoolVar(&opts.SkipZarfValidation, "skip-zarf-validation", false, "Skip Zarf Go-package validation pass")

	return cmd
}

func runGen(ctx context.Context, cmd *cobra.Command, opts genOptions, inputPath string) error {
	l := zlogger.From(ctx)

	dockerfilePath, contextPath := resolveBuildPaths(cmd, opts, inputPath)
	outputPath := filepath.Clean(opts.Output)

	if err := prepareOutput(outputPath); err != nil {
		return err
	}
	l.Info("rendering dist artifacts", "output", outputPath, "dockerfile", dockerfilePath, "context", contextPath)

	dockerfileSpec, err := dockerfile.ParseFile(dockerfilePath)
	if err != nil {
		return err
	}

	appName, err := normalizeAppName(dockerfileSpec.Name)
	if err != nil {
		return err
	}
	imageRef := fmt.Sprintf("keel.local/%s:latest", appName)
	namespace := appName

	app := model.AppSpec{
		Name:           appName,
		Namespace:      namespace,
		Image:          imageRef,
		Version:        opts.Version,
		DockerfilePath: dockerfilePath,
		ContextPath:    contextPath,
		Platforms:      defaultPlatforms,
		Dockerfile:     dockerfileSpec,
	}
	dist := model.NewDistSpec(outputPath)

	if err := render.Generate(render.Options{
		App:            app,
		Dist:           dist,
		ZarfMinVersion: opts.ZarfMinVersion,
	}); err != nil {
		return err
	}

	if !opts.SkipImageBuild {
		l.Info("building image archive", "image", imageRef, "platforms", strings.Join(defaultPlatforms, ","))
		if err := build.ImageArchive(ctx, build.Options{
			Dockerfile:    dockerfilePath,
			Context:       contextPath,
			Image:         imageRef,
			Platforms:     defaultPlatforms,
			OutputArchive: dist.ImageArchiveAbs,
			VerboseBuild:  isVerboseEnabled || strings.EqualFold(logLevelCLI, "debug"),
		}); err != nil {
			return err
		}
		l.Info("image archive created", "archive", dist.ImageArchiveAbs)
	}

	l.Info("validating dist artifacts")
	if err := validate.Dist(ctx, validate.Options{
		Dist:                dist,
		RequireImageArchive: !opts.SkipImageBuild,
		ValidateWithZarf:    !opts.SkipZarfValidation,
	}); err != nil {
		return err
	}

	l.Info("gen complete", "status", "SUCCESS", "output", dist.RootPath, "zarf_config", filepath.Join(dist.RootPath, "zarf.yaml"), "image_archive", dist.ImageArchiveAbs)
	return nil
}

func resolveBuildPaths(cmd *cobra.Command, opts genOptions, inputPath string) (dockerfilePath string, contextPath string) {
	contextPath = filepath.Clean(opts.Context)
	dockerfilePath = filepath.Clean(opts.Dockerfile)

	if inputPath != "" {
		if !cmd.Flags().Changed("context") {
			contextPath = filepath.Clean(inputPath)
		}
		if !cmd.Flags().Changed("dockerfile") {
			dockerfilePath = filepath.Join(filepath.Clean(inputPath), "Dockerfile")
		}
	}

	return dockerfilePath, contextPath
}

func prepareOutput(output string) error {
	if info, err := os.Stat(output); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("output path %s exists and is not a directory", output)
		}
		if err := os.RemoveAll(output); err != nil {
			return fmt.Errorf("remove existing output directory %s: %w", output, err)
		}
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create output directory %s: %w", output, err)
	}
	return nil
}

var invalidNameRunes = regexp.MustCompile(`[^a-z0-9.-]+`)

func normalizeAppName(name string) (string, error) {
	out := strings.ToLower(strings.TrimSpace(name))
	out = strings.ReplaceAll(out, "_", "-")
	out = invalidNameRunes.ReplaceAllString(out, "-")
	out = strings.Trim(out, "-.")
	if out == "" {
		return "", fmt.Errorf("Dockerfile LABEL NAME must contain at least one alphanumeric character")
	}
	return out, nil
}
