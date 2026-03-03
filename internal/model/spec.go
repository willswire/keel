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

type DockerfileSpec struct {
	ExposedPorts []Port
	Env          []EnvVar
	Name         string
	User         string
	Cmd          string
	Entrypoint   string
	Healthcheck  string
}

type AppSpec struct {
	Name           string
	Namespace      string
	Image          string
	Version        string
	DockerfilePath string
	ContextPath    string
	Platforms      []string
	Dockerfile     DockerfileSpec
}

type ComposeBuildSpec struct {
	ContextPath    string
	DockerfilePath string
}

type ComposeServiceSpec struct {
	Name       string
	Namespace  string
	Image      string
	Dockerfile DockerfileSpec
	Build      *ComposeBuildSpec
}

type ComposeAppSpec struct {
	Name            string
	Namespace       string
	Version         string
	ComposeFilePath string
	Services        []ComposeServiceSpec
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
	if len(a.Dockerfile.ExposedPorts) == 0 {
		return Port{}, fmt.Errorf("no EXPOSE instruction found in Dockerfile")
	}
	return a.Dockerfile.ExposedPorts[0], nil
}

func (s ComposeServiceSpec) PrimaryPort() (Port, error) {
	if len(s.Dockerfile.ExposedPorts) == 0 {
		return Port{}, fmt.Errorf("no EXPOSE or ports entry found for service %q", s.Name)
	}
	return s.Dockerfile.ExposedPorts[0], nil
}
