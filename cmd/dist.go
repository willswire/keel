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
	"github.com/willswire/keel/internal/compose"
	"github.com/willswire/keel/internal/dockerfile"
	"github.com/willswire/keel/internal/model"
	"github.com/willswire/keel/internal/render"
	"github.com/willswire/keel/internal/validate"
)

var defaultPlatforms = []string{"linux/amd64", "linux/arm64"}
var composeDefaultFilenames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

type genSource string

const (
	sourceDockerfile genSource = "dockerfile"
	sourceCompose    genSource = "compose"
)

type genOptions struct {
	Dockerfile         string
	ComposeFile        string
	ComposeProfiles    []string
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
		Short:   "Generate a .dist package layout from Dockerfile or Docker Compose",
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
	cmd.Flags().StringVar(&opts.ComposeFile, "compose-file", "", "Path to docker-compose.yml file")
	cmd.Flags().StringArrayVar(&opts.ComposeProfiles, "compose-profile", nil, "Compose profile to include (repeatable)")
	cmd.Flags().StringVar(&opts.Context, "context", ".", "Docker build context")
	cmd.Flags().StringVar(&opts.Version, "version", model.DefaultVersion, "Package version written to zarf.yaml")
	cmd.Flags().StringVar(&opts.Output, "output", ".dist", "Output directory")
	cmd.Flags().StringVar(&opts.ZarfMinVersion, "zarf-min-version", "v0.67.0", "Documented minimum Zarf version for image archive support")
	cmd.Flags().BoolVar(&opts.SkipImageBuild, "skip-image-build", false, "Skip docker buildx image archive build")
	cmd.Flags().BoolVar(&opts.SkipZarfValidation, "skip-zarf-validation", false, "Skip Zarf Go-package validation pass")

	return cmd
}

func runGen(ctx context.Context, cmd *cobra.Command, opts genOptions, inputPath string) error {
	source, dockerfilePath, contextPath, composePath, err := resolveInputSource(cmd, opts, inputPath)
	if err != nil {
		return err
	}
	outputPath := filepath.Clean(opts.Output)

	if err := prepareOutput(outputPath); err != nil {
		return err
	}

	if source == sourceCompose {
		return runGenCompose(ctx, opts, outputPath, composePath)
	}
	return runGenDockerfile(ctx, opts, outputPath, dockerfilePath, contextPath)
}

func runGenDockerfile(ctx context.Context, opts genOptions, outputPath string, dockerfilePath string, contextPath string) error {
	l := zlogger.From(ctx)
	l.Info("rendering dist artifacts", "source", "dockerfile", "output", outputPath, "dockerfile", dockerfilePath, "context", contextPath)

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

func runGenCompose(ctx context.Context, opts genOptions, outputPath string, composePath string) error {
	l := zlogger.From(ctx)
	l.Info("rendering dist artifacts", "source", "compose", "output", outputPath, "compose_file", composePath)

	composeSpec, err := compose.ParseFileWithOptions(composePath, compose.ParseOptions{
		Profiles: opts.ComposeProfiles,
	})
	if err != nil {
		return err
	}
	composeSpec.Version = opts.Version

	dist := model.NewDistSpec(outputPath)
	if err := render.GenerateCompose(render.ComposeOptions{
		App:  composeSpec,
		Dist: dist,
	}); err != nil {
		return err
	}

	archivePaths := []string{}
	if !opts.SkipImageBuild {
		for _, svc := range composeSpec.Services {
			if svc.Build == nil {
				continue
			}
			outputArchive := filepath.Join(dist.RootPath, "images", fmt.Sprintf("%s.tar", svc.Name))
			l.Info("building image archive", "service", svc.Name, "image", svc.Image, "platforms", strings.Join(defaultPlatforms, ","))
			if err := build.ImageArchive(ctx, build.Options{
				Dockerfile:    svc.Build.DockerfilePath,
				Context:       svc.Build.ContextPath,
				Image:         svc.Image,
				Platforms:     defaultPlatforms,
				OutputArchive: outputArchive,
				VerboseBuild:  isVerboseEnabled || strings.EqualFold(logLevelCLI, "debug"),
			}); err != nil {
				return err
			}
			archivePaths = append(archivePaths, outputArchive)
		}
	}

	l.Info("validating dist artifacts")
	if err := validate.ComposeDist(ctx, validate.ComposeOptions{
		Dist:                     dist,
		App:                      composeSpec,
		RequireBuiltImageArchive: !opts.SkipImageBuild,
		ValidateWithZarf:         !opts.SkipZarfValidation,
	}); err != nil {
		return err
	}

	l.Info("gen complete", "status", "SUCCESS", "output", dist.RootPath, "zarf_config", filepath.Join(dist.RootPath, "zarf.yaml"), "compose_file", composePath, "image_archives_built", len(archivePaths))
	return nil
}

func resolveBuildPaths(cmd *cobra.Command, opts genOptions, inputPath string) (dockerfilePath string, contextPath string) {
	contextPath = filepath.Clean(opts.Context)
	dockerfilePath = filepath.Clean(opts.Dockerfile)
	inputPath = filepath.Clean(inputPath)

	if looksLikeDockerfile(filepath.Base(inputPath)) {
		if !cmd.Flags().Changed("context") {
			contextPath = filepath.Dir(inputPath)
		}
		if !cmd.Flags().Changed("dockerfile") {
			dockerfilePath = inputPath
		}
		return dockerfilePath, contextPath
	}

	if inputPath != "" {
		if info, err := os.Stat(inputPath); err == nil && !info.IsDir() {
			if !cmd.Flags().Changed("context") {
				contextPath = filepath.Dir(inputPath)
			}
			if !cmd.Flags().Changed("dockerfile") {
				dockerfilePath = filepath.Clean(inputPath)
			}
			return dockerfilePath, contextPath
		}
		if !cmd.Flags().Changed("context") {
			contextPath = filepath.Clean(inputPath)
		}
		if !cmd.Flags().Changed("dockerfile") {
			dockerfilePath = filepath.Join(filepath.Clean(inputPath), "Dockerfile")
		}
	}

	return dockerfilePath, contextPath
}

func resolveComposePath(cmd *cobra.Command, opts genOptions, inputPath string) string {
	basePath := inputBasePath(inputPath)
	if basePath == "" {
		basePath = "."
	}

	composePath := opts.ComposeFile
	if !cmd.Flags().Changed("compose-file") || strings.TrimSpace(composePath) == "" {
		return filepath.Join(basePath, "docker-compose.yml")
	}
	composePath = filepath.Clean(composePath)
	if filepath.IsAbs(composePath) {
		return composePath
	}
	return filepath.Join(basePath, composePath)
}

func resolveInputSource(cmd *cobra.Command, opts genOptions, inputPath string) (genSource, string, string, string, error) {
	inputPath = filepath.Clean(inputPath)
	if inputPath == "" {
		inputPath = "."
	}

	dockerfilePath, contextPath := resolveBuildPaths(cmd, opts, inputPath)

	if cmd.Flags().Changed("compose-file") {
		return sourceCompose, "", "", resolveComposePath(cmd, opts, inputPath), nil
	}
	if cmd.Flags().Changed("dockerfile") {
		return sourceDockerfile, dockerfilePath, contextPath, "", nil
	}

	if info, err := os.Stat(inputPath); err == nil && !info.IsDir() {
		base := filepath.Base(inputPath)
		lower := strings.ToLower(base)
		if looksLikeDockerfile(base) {
			return sourceDockerfile, inputPath, filepath.Dir(inputPath), "", nil
		}
		if isComposeFilename(lower) || isYAMLFile(lower) {
			return sourceCompose, "", "", inputPath, nil
		}
		return "", "", "", "", fmt.Errorf("input path %s is a file but is not recognized as Dockerfile or compose YAML; use --dockerfile or --compose-file", inputPath)
	} else if err != nil && os.IsNotExist(err) {
		base := filepath.Base(inputPath)
		lower := strings.ToLower(base)
		if looksLikeDockerfile(base) {
			return sourceDockerfile, inputPath, filepath.Dir(inputPath), "", nil
		}
		if isComposeFilename(lower) || isYAMLFile(lower) {
			return sourceCompose, "", "", inputPath, nil
		}
	} else if err != nil {
		return "", "", "", "", fmt.Errorf("stat input path %s: %w", inputPath, err)
	}

	composePath := detectComposePath(inputPath)
	if composePath != "" && fileExists(dockerfilePath) {
		return "", "", "", "", fmt.Errorf("ambiguous input in %s: found both %s and %s; use --dockerfile or --compose-file", inputPath, dockerfilePath, composePath)
	}
	if composePath != "" {
		return sourceCompose, "", "", composePath, nil
	}
	return sourceDockerfile, dockerfilePath, contextPath, "", nil
}

func detectComposePath(inputPath string) string {
	basePath := inputBasePath(inputPath)
	for _, name := range composeDefaultFilenames {
		path := filepath.Join(basePath, name)
		if fileExists(path) {
			return path
		}
	}
	return ""
}

func inputBasePath(inputPath string) string {
	basePath := filepath.Clean(inputPath)
	if info, err := os.Stat(basePath); err == nil && !info.IsDir() {
		return filepath.Dir(basePath)
	}
	baseName := filepath.Base(basePath)
	if looksLikeDockerfile(baseName) || isComposeFilename(strings.ToLower(baseName)) || isYAMLFile(baseName) {
		return filepath.Dir(basePath)
	}
	return basePath
}

func looksLikeDockerfile(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return lower == "dockerfile" || strings.HasPrefix(lower, "dockerfile.")
}

func isComposeFilename(lowerName string) bool {
	for _, name := range composeDefaultFilenames {
		if lowerName == name {
			return true
		}
	}
	return false
}

func isYAMLFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}

func fileExists(path string) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
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
