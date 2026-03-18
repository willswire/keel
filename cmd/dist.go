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
	"github.com/willswire/keel/internal/containerfile"
	"github.com/willswire/keel/internal/model"
	"github.com/willswire/keel/internal/render"
	"github.com/willswire/keel/internal/validate"
)

var defaultPlatforms = []string{"linux/amd64", "linux/arm64"}
var composeDefaultFilenames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}
var buildFileDefaultFilenames = []string{"Containerfile", "Dockerfile"}

type genSource string

const (
	sourceContainerfile genSource = "containerfile"
	sourceCompose       genSource = "compose"
)

type genOptions struct {
	Containerfile      string
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
		Short:   "Generate a .dist package layout from Containerfile or Compose",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inputPath := "."
			if len(args) == 1 {
				inputPath = filepath.Clean(args[0])
			}
			return runGen(cmd.Context(), cmd, *opts, inputPath)
		},
	}

	cmd.Flags().StringVar(&opts.Containerfile, "containerfile", "./Containerfile", "Path to Containerfile")
	cmd.Flags().StringVar(&opts.ComposeFile, "compose-file", "", "Path to compose.yaml")
	cmd.Flags().StringArrayVar(&opts.ComposeProfiles, "compose-profile", nil, "Compose profile to include (repeatable)")
	cmd.Flags().StringVar(&opts.Context, "context", ".", "Container build context")
	cmd.Flags().StringVar(&opts.Version, "version", model.DefaultVersion, "Package version written to zarf.yaml")
	cmd.Flags().StringVar(&opts.Output, "output", ".dist", "Output directory")
	cmd.Flags().StringVar(&opts.ZarfMinVersion, "zarf-min-version", "v0.67.0", "Documented minimum Zarf version for image archive support")
	cmd.Flags().BoolVar(&opts.SkipImageBuild, "skip-image-build", false, "Skip image archive build")
	cmd.Flags().BoolVar(&opts.SkipZarfValidation, "skip-zarf-validation", false, "Skip Zarf Go-package validation pass")

	return cmd
}

func runGen(ctx context.Context, cmd *cobra.Command, opts genOptions, inputPath string) error {
	source, containerfilePath, contextPath, composePath, err := resolveInputSource(cmd, opts, inputPath)
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
	return runGenContainerfile(ctx, opts, outputPath, containerfilePath, contextPath)
}

func runGenContainerfile(ctx context.Context, opts genOptions, outputPath string, containerfilePath string, contextPath string) error {
	l := zlogger.From(ctx)
	l.Info("rendering dist artifacts", "source", "containerfile", "output", outputPath, "containerfile", containerfilePath, "context", contextPath)

	containerSpec, err := containerfile.ParseFile(containerfilePath)
	if err != nil {
		return err
	}

	appName, err := normalizeAppName(containerSpec.Name)
	if err != nil {
		return err
	}
	imageRef := fmt.Sprintf("keel.local/%s:latest", appName)
	namespace := appName

	app := model.AppSpec{
		Name:              appName,
		Namespace:         namespace,
		Image:             imageRef,
		Version:           opts.Version,
		ContainerfilePath: containerfilePath,
		ContextPath:       contextPath,
		Platforms:         defaultPlatforms,
		Container:         containerSpec,
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
			Containerfile: containerfilePath,
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
				Containerfile: svc.Build.ContainerfilePath,
				Context:       svc.Build.ContextPath,
				Image:         svc.Image,
				Platforms:     defaultPlatforms,
				OutputArchive: outputArchive,
				Target:        svc.Build.Target,
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

func resolveBuildPaths(cmd *cobra.Command, opts genOptions, inputPath string) (containerfilePath string, contextPath string) {
	contextPath = filepath.Clean(opts.Context)
	containerfilePath = filepath.Clean(opts.Containerfile)
	inputPath = filepath.Clean(inputPath)

	if looksLikeBuildFile(filepath.Base(inputPath)) {
		if !cmd.Flags().Changed("context") {
			contextPath = filepath.Dir(inputPath)
		}
		if !cmd.Flags().Changed("containerfile") {
			containerfilePath = inputPath
		}
		return containerfilePath, contextPath
	}

	if inputPath != "" {
		if info, err := os.Stat(inputPath); err == nil && !info.IsDir() {
			if !cmd.Flags().Changed("context") {
				contextPath = filepath.Dir(inputPath)
			}
			if !cmd.Flags().Changed("containerfile") {
				containerfilePath = filepath.Clean(inputPath)
			}
			return containerfilePath, contextPath
		}
		if !cmd.Flags().Changed("context") {
			contextPath = filepath.Clean(inputPath)
		}
		if !cmd.Flags().Changed("containerfile") {
			if detected := detectBuildPath(inputPath); detected != "" {
				containerfilePath = detected
			} else {
				containerfilePath = filepath.Join(filepath.Clean(inputPath), "Containerfile")
			}
		}
	}

	return containerfilePath, contextPath
}

func resolveComposePath(cmd *cobra.Command, opts genOptions, inputPath string) string {
	basePath := inputBasePath(inputPath)
	if basePath == "" {
		basePath = "."
	}

	composePath := opts.ComposeFile
	if !cmd.Flags().Changed("compose-file") || strings.TrimSpace(composePath) == "" {
		return filepath.Join(basePath, composeDefaultFilenames[0])
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

	containerfilePath, contextPath := resolveBuildPaths(cmd, opts, inputPath)

	if cmd.Flags().Changed("compose-file") {
		return sourceCompose, "", "", resolveComposePath(cmd, opts, inputPath), nil
	}
	if cmd.Flags().Changed("containerfile") {
		return sourceContainerfile, containerfilePath, contextPath, "", nil
	}

	if info, err := os.Stat(inputPath); err == nil && !info.IsDir() {
		base := filepath.Base(inputPath)
		lower := strings.ToLower(base)
		if looksLikeBuildFile(base) {
			return sourceContainerfile, inputPath, filepath.Dir(inputPath), "", nil
		}
		if isComposeFilename(lower) || isYAMLFile(lower) {
			return sourceCompose, "", "", inputPath, nil
		}
		return "", "", "", "", fmt.Errorf("input path %s is a file but is not recognized as a container build file or compose YAML; use --containerfile or --compose-file", inputPath)
	} else if err != nil && os.IsNotExist(err) {
		base := filepath.Base(inputPath)
		lower := strings.ToLower(base)
		if looksLikeBuildFile(base) {
			return sourceContainerfile, inputPath, filepath.Dir(inputPath), "", nil
		}
		if isComposeFilename(lower) || isYAMLFile(lower) {
			return sourceCompose, "", "", inputPath, nil
		}
	} else if err != nil {
		return "", "", "", "", fmt.Errorf("stat input path %s: %w", inputPath, err)
	}

	composePath := detectComposePath(inputPath)
	buildPaths := detectBuildPaths(inputPath)
	if len(buildPaths) > 1 {
		return "", "", "", "", fmt.Errorf("ambiguous input in %s: found both %s and %s; use --containerfile to choose one", inputPath, buildPaths[0], buildPaths[1])
	}
	if composePath != "" && len(buildPaths) == 1 {
		return "", "", "", "", fmt.Errorf("ambiguous input in %s: found both %s and %s; use --containerfile or --compose-file", inputPath, buildPaths[0], composePath)
	}
	if composePath != "" {
		return sourceCompose, "", "", composePath, nil
	}
	if len(buildPaths) == 1 {
		return sourceContainerfile, buildPaths[0], contextPath, "", nil
	}
	return sourceContainerfile, containerfilePath, contextPath, "", nil
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
	if looksLikeBuildFile(baseName) || isComposeFilename(strings.ToLower(baseName)) || isYAMLFile(baseName) {
		return filepath.Dir(basePath)
	}
	return basePath
}

func looksLikeBuildFile(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return lower == "dockerfile" ||
		strings.HasPrefix(lower, "dockerfile.") ||
		lower == "containerfile" ||
		strings.HasPrefix(lower, "containerfile.")
}

func detectBuildPath(inputPath string) string {
	paths := detectBuildPaths(inputPath)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func detectBuildPaths(inputPath string) []string {
	basePath := inputBasePath(inputPath)
	out := make([]string, 0, len(buildFileDefaultFilenames))
	for _, name := range buildFileDefaultFilenames {
		path := filepath.Join(basePath, name)
		if fileExists(path) {
			out = append(out, path)
		}
	}
	return out
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
		return "", fmt.Errorf("LABEL NAME must contain at least one alphanumeric character")
	}
	return out, nil
}
