package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/zarf-dev/zarf/src/api/v1alpha1"

	"github.com/willswire/keel/internal/model"
	"github.com/willswire/keel/internal/zarf"
)

type Options struct {
	Dist                model.DistSpec
	RequireImageArchive bool
	ValidateWithZarf    bool
}

func Dist(ctx context.Context, opts Options) error {
	requiredFiles := []string{
		filepath.Join(opts.Dist.ManifestDir, "namespace.yaml"),
		filepath.Join(opts.Dist.ManifestDir, "deployment.yaml"),
		filepath.Join(opts.Dist.ManifestDir, "service.yaml"),
		filepath.Join(opts.Dist.ManifestDir, "uds-package.yaml"),
		filepath.Join(opts.Dist.RootPath, "zarf.yaml"),
	}
	if opts.RequireImageArchive {
		requiredFiles = append(requiredFiles, opts.Dist.ImageArchiveAbs)
	}
	for _, f := range requiredFiles {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("missing required dist artifact %s: %w", f, err)
		}
	}

	deploymentImage, err := deploymentImage(filepath.Join(opts.Dist.ManifestDir, "deployment.yaml"))
	if err != nil {
		return err
	}

	zarfConfig, err := readZarfConfig(filepath.Join(opts.Dist.RootPath, "zarf.yaml"))
	if err != nil {
		return err
	}
	if err := checkZarfConsistency(opts.Dist, zarfConfig, deploymentImage); err != nil {
		return err
	}

	if opts.ValidateWithZarf {
		if err := zarf.ValidateDefinition(ctx, opts.Dist.RootPath); err != nil {
			return err
		}
	}

	return nil
}

func readZarfConfig(path string) (v1alpha1.ZarfPackage, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return v1alpha1.ZarfPackage{}, fmt.Errorf("read zarf config %s: %w", path, err)
	}
	var pkg v1alpha1.ZarfPackage
	if err := yaml.Unmarshal(content, &pkg); err != nil {
		return v1alpha1.ZarfPackage{}, fmt.Errorf("parse zarf config %s: %w", path, err)
	}
	return pkg, nil
}

func checkZarfConsistency(dist model.DistSpec, pkg v1alpha1.ZarfPackage, expectedImage string) error {
	if pkg.Kind != v1alpha1.ZarfPackageConfig {
		return fmt.Errorf("zarf kind must be %q, found %q", v1alpha1.ZarfPackageConfig, pkg.Kind)
	}
	if len(pkg.Components) == 0 {
		return fmt.Errorf("zarf config must define at least one component")
	}

	archivePath := filepath.Join(dist.RootPath, dist.ImageArchiveRel)
	imageFound := false
	archiveReferenced := false
	for _, c := range pkg.Components {
		for _, a := range c.ImageArchives {
			resolved := filepath.Join(dist.RootPath, a.Path)
			if resolved == archivePath {
				archiveReferenced = true
			}
			for _, image := range a.Images {
				if image == expectedImage {
					imageFound = true
				}
			}
		}
	}
	if !archiveReferenced {
		return fmt.Errorf("zarf config does not reference required image archive path %s", dist.ImageArchiveRel)
	}
	if !imageFound {
		return fmt.Errorf("deployment image %q is not listed under zarf imageArchives", expectedImage)
	}
	return nil
}

func deploymentImage(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read deployment manifest %s: %w", path, err)
	}

	var dep struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &dep); err != nil {
		return "", fmt.Errorf("parse deployment manifest %s: %w", path, err)
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return "", fmt.Errorf("deployment manifest must include at least one container")
	}
	image := dep.Spec.Template.Spec.Containers[0].Image
	if image == "" {
		return "", fmt.Errorf("deployment manifest first container image is empty")
	}
	return image, nil
}
