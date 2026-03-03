package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zarf-dev/zarf/src/api/v1alpha1"

	"github.com/willswire/keel/internal/model"
	"github.com/willswire/keel/internal/zarf"
)

type ComposeOptions struct {
	Dist                     model.DistSpec
	App                      model.ComposeAppSpec
	RequireBuiltImageArchive bool
	ValidateWithZarf         bool
}

func ComposeDist(ctx context.Context, opts ComposeOptions) error {
	requiredFiles := []string{
		filepath.Join(opts.Dist.ManifestDir, "namespace.yaml"),
		filepath.Join(opts.Dist.RootPath, "zarf.yaml"),
	}
	for _, svc := range opts.App.Services {
		requiredFiles = append(requiredFiles,
			filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("deployment-%s.yaml", svc.Name)),
		)
		if len(svc.Dockerfile.ExposedPorts) > 0 {
			requiredFiles = append(requiredFiles,
				filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("service-%s.yaml", svc.Name)),
				filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("uds-package-%s.yaml", svc.Name)),
			)
		}
		if opts.RequireBuiltImageArchive && svc.Build != nil {
			requiredFiles = append(requiredFiles, filepath.Join(opts.Dist.RootPath, "images", fmt.Sprintf("%s.tar", svc.Name)))
		}
	}
	for _, f := range requiredFiles {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("missing required dist artifact %s: %w", f, err)
		}
	}

	expectedImages := make(map[string]string, len(opts.App.Services))
	for _, svc := range opts.App.Services {
		image, err := deploymentImage(filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("deployment-%s.yaml", svc.Name)))
		if err != nil {
			return err
		}
		expectedImages[svc.Name] = image
	}

	pkg, err := readZarfConfig(filepath.Join(opts.Dist.RootPath, "zarf.yaml"))
	if err != nil {
		return err
	}
	if err := checkComposeZarfConsistency(opts.App, pkg, expectedImages); err != nil {
		return err
	}

	if opts.ValidateWithZarf {
		if err := zarf.ValidateDefinition(ctx, opts.Dist.RootPath); err != nil {
			return err
		}
	}
	return nil
}

func checkComposeZarfConsistency(app model.ComposeAppSpec, pkg v1alpha1.ZarfPackage, expectedImages map[string]string) error {
	if pkg.Kind != v1alpha1.ZarfPackageConfig {
		return fmt.Errorf("zarf kind must be %q, found %q", v1alpha1.ZarfPackageConfig, pkg.Kind)
	}
	if len(pkg.Components) == 0 {
		return fmt.Errorf("zarf config must define at least one component")
	}

	componentByName := map[string]v1alpha1.ZarfComponent{}
	for _, component := range pkg.Components {
		componentByName[component.Name] = component
	}

	for _, svc := range app.Services {
		component, ok := componentByName[svc.Name]
		if !ok {
			return fmt.Errorf("zarf config is missing component %q", svc.Name)
		}

		requiredManifestFiles := []string{
			"manifests/namespace.yaml",
			fmt.Sprintf("manifests/deployment-%s.yaml", svc.Name),
		}
		if len(svc.Dockerfile.ExposedPorts) > 0 {
			requiredManifestFiles = append(requiredManifestFiles,
				fmt.Sprintf("manifests/service-%s.yaml", svc.Name),
				fmt.Sprintf("manifests/uds-package-%s.yaml", svc.Name),
			)
		}
		if err := ensureComponentManifests(component, requiredManifestFiles); err != nil {
			return err
		}

		expectedImage, ok := expectedImages[svc.Name]
		if !ok {
			return fmt.Errorf("missing expected deployment image for service %q", svc.Name)
		}
		imageFound := false
		for _, image := range component.GetImages() {
			if image == expectedImage {
				imageFound = true
				break
			}
		}
		if !imageFound {
			return fmt.Errorf("deployment image %q is not referenced in zarf component %q", expectedImage, svc.Name)
		}

		if svc.Build != nil {
			archivePath := filepath.ToSlash(filepath.Join("images", fmt.Sprintf("%s.tar", svc.Name)))
			archiveFound := false
			for _, archive := range component.ImageArchives {
				if filepath.ToSlash(archive.Path) == archivePath {
					archiveFound = true
					break
				}
			}
			if !archiveFound {
				return fmt.Errorf("zarf component %q does not reference required image archive %s", svc.Name, archivePath)
			}
		}
	}

	return nil
}

func ensureComponentManifests(component v1alpha1.ZarfComponent, requiredFiles []string) error {
	manifestFiles := map[string]struct{}{}
	for _, manifest := range component.Manifests {
		for _, file := range manifest.Files {
			manifestFiles[filepath.ToSlash(file)] = struct{}{}
		}
	}
	for _, required := range requiredFiles {
		if _, ok := manifestFiles[filepath.ToSlash(required)]; !ok {
			return fmt.Errorf("zarf component %q missing manifest file %s", component.Name, required)
		}
	}
	return nil
}
