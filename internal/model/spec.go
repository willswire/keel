package model

import "fmt"

const (
	DefaultVersion         = "0.1.0"
	DefaultImageArchiveRel = "images/app.tar"
)

type Port struct {
	Number   int
	Protocol string
	Raw      string
}

type EnvVar struct {
	Name  string
	Value string
}

type ContainerSpec struct {
	ExposedPorts []Port
	Env          []EnvVar
	Name         string
	User         string
	Cmd          string
	Entrypoint   string
	Healthcheck  string
}

type AppSpec struct {
	Name              string
	Namespace         string
	Image             string
	Version           string
	ContainerfilePath string
	ContextPath       string
	Platforms         []string
	Container         ContainerSpec
}

type ComposeBuildSpec struct {
	ContextPath       string
	ContainerfilePath string
	Target            string
}

type ComposeVolumeSpec struct {
	Name     string
	External bool
}

type ComposeVolumeMount struct {
	Name       string
	Type       string
	Source     string
	SourcePath string
	Target     string
	ReadOnly   bool
}

type ComposeSecretSpec struct {
	Name        string
	External    bool
	FilePath    string
	Environment string
}

type ComposeServiceSecretSpec struct {
	Source string
	Target string
}

type ComposeDependencySpec struct {
	Service   string
	Condition string
}

type ComposeResourceSet struct {
	CPU    string
	Memory string
}

type ComposeResourcesSpec struct {
	Limits   ComposeResourceSet
	Requests ComposeResourceSet
}

type ComposeServiceSpec struct {
	Name      string
	Namespace string
	Image     string
	Container ContainerSpec
	Build     *ComposeBuildSpec
	Volumes   []ComposeVolumeMount
	Secrets   []ComposeServiceSecretSpec
	DependsOn []ComposeDependencySpec
	Resources ComposeResourcesSpec
	Profiles  []string
}

type ComposeAppSpec struct {
	Name            string
	Namespace       string
	Version         string
	ComposeFilePath string
	Services        []ComposeServiceSpec
	Volumes         map[string]ComposeVolumeSpec
	Secrets         map[string]ComposeSecretSpec
}

type DistSpec struct {
	RootPath        string
	ManifestDir     string
	ImageDir        string
	ImageArchiveRel string
	ImageArchiveAbs string
}

func NewDistSpec(output string) DistSpec {
	return DistSpec{
		RootPath:        output,
		ManifestDir:     output + "/manifests",
		ImageDir:        output + "/images",
		ImageArchiveRel: DefaultImageArchiveRel,
		ImageArchiveAbs: output + "/" + DefaultImageArchiveRel,
	}
}

func (a AppSpec) PrimaryPort() (Port, error) {
	if len(a.Container.ExposedPorts) == 0 {
		return Port{}, fmt.Errorf("no EXPOSE instruction found in container build file")
	}
	return a.Container.ExposedPorts[0], nil
}

func (s ComposeServiceSpec) PrimaryPort() (Port, error) {
	if len(s.Container.ExposedPorts) == 0 {
		return Port{}, fmt.Errorf("no EXPOSE or ports entry found for service %q", s.Name)
	}
	return s.Container.ExposedPorts[0], nil
}
