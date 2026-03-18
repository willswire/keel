package render

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zarf-dev/zarf/src/api/v1alpha1"
	yamlv3 "gopkg.in/yaml.v3"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/willswire/keel/internal/model"
	"github.com/willswire/keel/internal/zarf"
)

const dependencyInitImage = "busybox:1.36"

type ComposeOptions struct {
	App  model.ComposeAppSpec
	Dist model.DistSpec
}

type composeComponentSpec struct {
	Name          string
	Namespace     string
	ManifestFiles []string
	Image         string
	ImageArchive  string
	UsesArchive   bool
	Images        []string
	DependsOn     []string
}

type composeSecretTemplateSpec struct {
	Name          string
	VariableName  string
	TemplateValue string
	Description   string
}

type composeExposeSpec struct {
	Service string
	Host    string
	Port    int
}

func GenerateCompose(opts ComposeOptions) error {
	if err := os.MkdirAll(opts.Dist.ManifestDir, 0o755); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	if err := os.MkdirAll(opts.Dist.ImageDir, 0o755); err != nil {
		return fmt.Errorf("create image directory: %w", err)
	}

	if err := writeTemplate(filepath.Join(opts.Dist.ManifestDir, "namespace.yaml"), namespaceTemplate, map[string]any{
		"Namespace": opts.App.Namespace,
	}); err != nil {
		return err
	}

	secretManifestByName := map[string]string{}
	secretTemplateByName := map[string]composeSecretTemplateSpec{}
	usedSecretVariables := map[string]struct{}{}
	for _, name := range sortedSecretNames(opts.App.Secrets) {
		spec := opts.App.Secrets[name]
		if spec.External {
			continue
		}
		variableName := buildComposeSecretVariableName(spec.Name, usedSecretVariables)
		description := fmt.Sprintf("Value for compose secret %s", spec.Name)
		if spec.Environment != "" {
			description = fmt.Sprintf("%s (compose source environment: %s)", description, spec.Environment)
		} else if spec.FilePath != "" {
			description = fmt.Sprintf("%s (compose source file: %s)", description, filepath.Base(spec.FilePath))
		}
		template := composeSecretTemplateSpec{
			Name:          spec.Name,
			VariableName:  variableName,
			TemplateValue: fmt.Sprintf("###ZARF_VAR_%s###", variableName),
			Description:   description,
		}
		relPath := filepath.ToSlash(filepath.Join("manifests", fmt.Sprintf("secret-%s.yaml", spec.Name)))
		if err := writeComposeSecretManifest(filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("secret-%s.yaml", spec.Name)), opts.App.Namespace, template); err != nil {
			return err
		}
		secretManifestByName[name] = relPath
		secretTemplateByName[name] = template
	}

	pvcManifestByName := map[string]string{}
	for _, name := range sortedVolumeNames(opts.App.Volumes) {
		spec := opts.App.Volumes[name]
		if spec.External {
			continue
		}
		relPath := filepath.ToSlash(filepath.Join("manifests", fmt.Sprintf("pvc-%s.yaml", spec.Name)))
		if err := writeComposePVCManifest(filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("pvc-%s.yaml", spec.Name)), opts.App.Namespace, spec.Name); err != nil {
			return err
		}
		pvcManifestByName[name] = relPath
	}

	servicePorts := map[string]int{}
	for _, svc := range opts.App.Services {
		if len(svc.Container.ExposedPorts) > 0 {
			servicePorts[svc.Name] = svc.Container.ExposedPorts[0].Number
		}
	}

	components := make([]composeComponentSpec, 0, len(opts.App.Services))
	packageExposes := []composeExposeSpec{}
	for _, svc := range opts.App.Services {
		ports := buildTemplatePorts(svc.Container.ExposedPorts)
		user := strings.TrimSpace(svc.Container.User)
		runAsNonRoot, hasRunAsUser, runAsUser := userSecurity(user)
		// Compose often omits "user"; default to non-root for UDS policy compatibility.
		if user == "" {
			runAsNonRoot = true
		}

		volumeMounts := []renderedVolumeMount{}
		volumes := []renderedVolume{}
		serviceManifestFiles := []string{"manifests/namespace.yaml"}

		for i, mount := range svc.Volumes {
			volumeName := fmt.Sprintf("volume-%d", i)
			switch strings.ToLower(strings.TrimSpace(mount.Type)) {
			case "volume":
				claimName := mount.Name
				if claimName == "" {
					return fmt.Errorf("service %q has volume mount at %q without resolved PVC name", svc.Name, mount.Target)
				}
				volumes = append(volumes, renderedVolume{Name: volumeName, PersistentVolumeClaim: claimName})
				volumeMounts = append(volumeMounts, renderedVolumeMount{
					Name:      volumeName,
					MountPath: mount.Target,
					ReadOnly:  mount.ReadOnly,
				})
				if manifestPath, ok := pvcManifestByName[claimName]; ok {
					serviceManifestFiles = append(serviceManifestFiles, manifestPath)
				}
			case "bind":
				bindVolume, bindMount, bindManifest, err := materializeBindMount(opts.Dist, opts.App.Namespace, svc.Name, i, mount)
				if err != nil {
					return err
				}
				volumes = append(volumes, bindVolume)
				volumeMounts = append(volumeMounts, bindMount)
				if bindManifest != "" {
					serviceManifestFiles = append(serviceManifestFiles, bindManifest)
				}
			default:
				return fmt.Errorf("service %q mount %q has unsupported type %q", svc.Name, mount.Target, mount.Type)
			}
		}

		for i, ref := range svc.Secrets {
			secretVolName := fmt.Sprintf("secret-%d", i)
			volumes = append(volumes, renderedVolume{Name: secretVolName, SecretName: ref.Source})
			volumeMounts = append(volumeMounts, renderedVolumeMount{
				Name:      secretVolName,
				MountPath: resolveSecretTargetPath(ref.Target),
				ReadOnly:  true,
				SubPath:   "value",
			})
			if manifestPath, ok := secretManifestByName[ref.Source]; ok {
				serviceManifestFiles = append(serviceManifestFiles, manifestPath)
			}
		}

		initContainers := buildDependencyInitContainers(svc, servicePorts)
		resources := buildRenderedResources(svc.Resources)

		deploymentRel := filepath.ToSlash(filepath.Join("manifests", fmt.Sprintf("deployment-%s.yaml", svc.Name)))
		if err := writeTemplate(filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("deployment-%s.yaml", svc.Name)), deploymentTemplate, map[string]any{
			"Name":           svc.Name,
			"Namespace":      svc.Namespace,
			"Image":          svc.Image,
			"Ports":          ports,
			"Env":            svc.Container.Env,
			"Command":        parseContainerArgs(svc.Container.Entrypoint),
			"Args":           parseContainerArgs(svc.Container.Cmd),
			"Healthcheck":    parseHealthcheck(svc.Container.Healthcheck),
			"RunAsNonRoot":   runAsNonRoot,
			"HasRunAsUser":   hasRunAsUser,
			"RunAsUser":      runAsUser,
			"InitContainers": initContainers,
			"VolumeMounts":   volumeMounts,
			"Volumes":        volumes,
			"Resources":      resources,
		}); err != nil {
			return err
		}
		serviceManifestFiles = append(serviceManifestFiles, deploymentRel)

		component := composeComponentSpec{
			Name:      svc.Name,
			Namespace: svc.Namespace,
			Image:     svc.Image,
		}
		for _, dep := range svc.DependsOn {
			component.DependsOn = append(component.DependsOn, dep.Service)
		}
		component.DependsOn = dedupeStrings(component.DependsOn)

		serviceRel := filepath.ToSlash(filepath.Join("manifests", fmt.Sprintf("service-%s.yaml", svc.Name)))
		if len(ports) > 0 {
			if err := writeTemplate(filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("service-%s.yaml", svc.Name)), serviceTemplate, map[string]any{
				"Name":      svc.Name,
				"Namespace": svc.Namespace,
				"Ports":     ports,
			}); err != nil {
				return err
			}
			serviceManifestFiles = append(serviceManifestFiles, serviceRel)

			primary, err := svc.PrimaryPort()
			if err != nil {
				return err
			}
			packageExposes = append(packageExposes, composeExposeSpec{
				Service: svc.Name,
				Host:    svc.Name,
				Port:    primary.Number,
			})
		} else {
			// No ports declared: generate a headless service so DNS resolution works
			// for inter-service communication (e.g. depends_on, proxy upstreams).
			if err := writeTemplate(filepath.Join(opts.Dist.ManifestDir, fmt.Sprintf("service-%s.yaml", svc.Name)), headlessServiceTemplate, map[string]any{
				"Name":      svc.Name,
				"Namespace": svc.Namespace,
			}); err != nil {
				return err
			}
			serviceManifestFiles = append(serviceManifestFiles, serviceRel)
		}

		component.ManifestFiles = dedupeStrings(serviceManifestFiles)

		if svc.Build != nil {
			component.UsesArchive = true
			component.ImageArchive = filepath.ToSlash(filepath.Join("images", fmt.Sprintf("%s.tar", svc.Name)))
		} else {
			component.Images = append(component.Images, svc.Image)
		}
		if len(initContainers) > 0 {
			component.Images = append(component.Images, dependencyInitImage)
		}
		component.Images = dedupeStrings(component.Images)
		components = append(components, component)
	}

	if len(packageExposes) > 0 {
		udsPackageRel := filepath.ToSlash(filepath.Join("manifests", "uds-package.yaml"))
		if err := writeTemplate(filepath.Join(opts.Dist.ManifestDir, "uds-package.yaml"), composeUDSPackageTemplate, map[string]any{
			"Name":      opts.App.Name,
			"Namespace": opts.App.Namespace,
			"Exposes":   packageExposes,
		}); err != nil {
			return err
		}
		if len(components) > 0 {
			last := len(components) - 1
			components[last].ManifestFiles = append(components[last].ManifestFiles, udsPackageRel)
			components[last].ManifestFiles = dedupeStrings(components[last].ManifestFiles)
		}
	}
	return writeComposeZarfConfig(opts, components, composeSecretVariables(secretTemplateByName))
}

func buildRenderedResources(spec model.ComposeResourcesSpec) renderedResources {
	resources := renderedResources{
		LimitCPU:      strings.TrimSpace(spec.Limits.CPU),
		LimitMemory:   strings.TrimSpace(spec.Limits.Memory),
		RequestCPU:    strings.TrimSpace(spec.Requests.CPU),
		RequestMemory: strings.TrimSpace(spec.Requests.Memory),
	}
	resources.HasLimits = resources.LimitCPU != "" || resources.LimitMemory != ""
	resources.HasRequests = resources.RequestCPU != "" || resources.RequestMemory != ""
	resources.HasAny = resources.HasLimits || resources.HasRequests
	return resources
}

func buildDependencyInitContainers(svc model.ComposeServiceSpec, servicePorts map[string]int) []renderedInitContainer {
	if len(svc.DependsOn) == 0 {
		return nil
	}
	containers := []renderedInitContainer{}
	for _, dep := range svc.DependsOn {
		port, ok := servicePorts[dep.Service]
		if !ok || port <= 0 {
			continue
		}
		waitScript := fmt.Sprintf("until nc -z %s %d; do echo waiting for %s:%d; sleep 2; done", dep.Service, port, dep.Service, port)
		containers = append(containers, renderedInitContainer{
			Name:    sanitizeManifestName("wait-" + dep.Service),
			Image:   dependencyInitImage,
			Command: []string{"sh", "-c", waitScript},
		})
	}
	return containers
}

func materializeBindMount(dist model.DistSpec, namespace string, serviceName string, index int, mount model.ComposeVolumeMount) (renderedVolume, renderedVolumeMount, string, error) {
	volumeName := fmt.Sprintf("volume-%d", index)
	sourcePath := strings.TrimSpace(mount.SourcePath)
	if sourcePath == "" {
		return renderedVolume{}, renderedVolumeMount{}, "", fmt.Errorf("service %q bind mount at %q has empty source path", serviceName, mount.Target)
	}

	// Kubernetes ConfigMap data is limited to 1MiB. Files or directories exceeding
	// this limit fall through to PVC creation below.
	const configMapSizeLimit = 900 * 1024 // 900 KiB — leave headroom for YAML overhead

	info, err := os.Stat(sourcePath)
	if err == nil {
		if info.Mode().IsRegular() && info.Size() <= configMapSizeLimit {
			data, err := os.ReadFile(sourcePath)
			if err != nil {
				return renderedVolume{}, renderedVolumeMount{}, "", fmt.Errorf("read bind file %s: %w", sourcePath, err)
			}
			configName := sanitizeManifestName(fmt.Sprintf("%s-bind-%d", serviceName, index))
			manifestRel := filepath.ToSlash(filepath.Join("manifests", fmt.Sprintf("configmap-%s.yaml", configName)))
			if err := writeComposeConfigMapManifest(filepath.Join(dist.ManifestDir, fmt.Sprintf("configmap-%s.yaml", configName)), namespace, configName, map[string]string{filepath.Base(sourcePath): string(data)}); err != nil {
				return renderedVolume{}, renderedVolumeMount{}, "", err
			}
			return renderedVolume{Name: volumeName, ConfigMapName: configName}, renderedVolumeMount{
				Name:      volumeName,
				MountPath: mount.Target,
				ReadOnly:  true,
				SubPath:   filepath.Base(sourcePath),
			}, manifestRel, nil
		}
		if info.IsDir() {
			entries, err := os.ReadDir(sourcePath)
			if err != nil {
				return renderedVolume{}, renderedVolumeMount{}, "", fmt.Errorf("read bind directory %s: %w", sourcePath, err)
			}
			data := map[string]string{}
			totalSize := 0
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				fi, err := entry.Info()
				if err != nil {
					continue
				}
				totalSize += int(fi.Size())
				if totalSize > configMapSizeLimit {
					data = nil
					break
				}
				bytes, err := os.ReadFile(filepath.Join(sourcePath, entry.Name()))
				if err != nil {
					return renderedVolume{}, renderedVolumeMount{}, "", fmt.Errorf("read bind file %s: %w", filepath.Join(sourcePath, entry.Name()), err)
				}
				data[entry.Name()] = string(bytes)
			}
			if len(data) > 0 {
				configName := sanitizeManifestName(fmt.Sprintf("%s-bind-%d", serviceName, index))
				manifestRel := filepath.ToSlash(filepath.Join("manifests", fmt.Sprintf("configmap-%s.yaml", configName)))
				if err := writeComposeConfigMapManifest(filepath.Join(dist.ManifestDir, fmt.Sprintf("configmap-%s.yaml", configName)), namespace, configName, data); err != nil {
					return renderedVolume{}, renderedVolumeMount{}, "", err
				}
				return renderedVolume{Name: volumeName, ConfigMapName: configName}, renderedVolumeMount{
					Name:      volumeName,
					MountPath: mount.Target,
					ReadOnly:  true,
				}, manifestRel, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return renderedVolume{}, renderedVolumeMount{}, "", fmt.Errorf("stat bind source %s: %w", sourcePath, err)
	}

	claimName := sanitizeManifestName(fmt.Sprintf("%s-bind-%d", serviceName, index))
	manifestRel := filepath.ToSlash(filepath.Join("manifests", fmt.Sprintf("pvc-%s.yaml", claimName)))
	if err := writeComposePVCManifest(filepath.Join(dist.ManifestDir, fmt.Sprintf("pvc-%s.yaml", claimName)), namespace, claimName); err != nil {
		return renderedVolume{}, renderedVolumeMount{}, "", err
	}
	return renderedVolume{Name: volumeName, PersistentVolumeClaim: claimName}, renderedVolumeMount{
		Name:      volumeName,
		MountPath: mount.Target,
		ReadOnly:  mount.ReadOnly,
	}, manifestRel, nil
}

func writeComposePVCManifest(path string, namespace string, claimName string) error {
	manifest := struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Spec struct {
			AccessModes []string `yaml:"accessModes"`
			Resources   struct {
				Requests struct {
					Storage string `yaml:"storage"`
				} `yaml:"requests"`
			} `yaml:"resources"`
		} `yaml:"spec"`
	}{}
	manifest.APIVersion = "v1"
	manifest.Kind = "PersistentVolumeClaim"
	manifest.Metadata.Name = claimName
	manifest.Metadata.Namespace = namespace
	manifest.Spec.AccessModes = []string{"ReadWriteOnce"}
	manifest.Spec.Resources.Requests.Storage = "1Gi"
	return writeYAMLManifest(path, manifest)
}

func writeComposeSecretManifest(path string, namespace string, secret composeSecretTemplateSpec) error {
	manifest := struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Type       string            `yaml:"type"`
		StringData map[string]string `yaml:"stringData"`
	}{}
	manifest.APIVersion = "v1"
	manifest.Kind = "Secret"
	manifest.Metadata.Name = secret.Name
	manifest.Metadata.Namespace = namespace
	manifest.Type = "Opaque"
	manifest.StringData = map[string]string{"value": secret.TemplateValue}
	return writeYAMLManifest(path, manifest)
}

func writeComposeConfigMapManifest(path string, namespace string, name string, data map[string]string) error {
	manifest := struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}{}
	manifest.APIVersion = "v1"
	manifest.Kind = "ConfigMap"
	manifest.Metadata.Name = name
	manifest.Metadata.Namespace = namespace
	manifest.Data = data
	return writeYAMLManifest(path, manifest)
}

func writeYAMLManifest(path string, value any) error {
	data, err := yamlv3.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal yaml for %s: %w", path, err)
	}
	return os.WriteFile(path, data, 0o644)
}

func resolveSecretTargetPath(target string) string {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return "/run/secrets/value"
	}
	if strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	return filepath.ToSlash(filepath.Join("/run/secrets", trimmed))
}

func sortedVolumeNames(volumes map[string]model.ComposeVolumeSpec) []string {
	keys := make([]string, 0, len(volumes))
	for key := range volumes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSecretNames(secrets map[string]model.ComposeSecretSpec) []string {
	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func buildComposeSecretVariableName(secretName string, used map[string]struct{}) string {
	base := strings.ToUpper(strings.TrimSpace(secretName))
	base = strings.ReplaceAll(base, "-", "_")
	base = strings.ReplaceAll(base, ".", "_")
	base = invalidSecretVariableRunes.ReplaceAllString(base, "_")
	base = strings.Trim(base, "_")
	if base == "" {
		base = "VALUE"
	}
	candidate := base
	for i := 2; ; i++ {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s_%d", base, i)
	}
}

func composeSecretVariables(secretTemplateByName map[string]composeSecretTemplateSpec) []v1alpha1.InteractiveVariable {
	keys := make([]string, 0, len(secretTemplateByName))
	for key := range secretTemplateByName {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	variables := make([]v1alpha1.InteractiveVariable, 0, len(keys))
	for _, key := range keys {
		secret := secretTemplateByName[key]
		variables = append(variables, v1alpha1.InteractiveVariable{
			Variable: v1alpha1.Variable{
				Name:      secret.VariableName,
				Sensitive: true,
			},
			Description: secret.Description,
			Prompt:      true,
		})
	}
	return variables
}

var invalidManifestNameRunes = regexp.MustCompile(`[^a-z0-9.-]+`)
var invalidSecretVariableRunes = regexp.MustCompile(`[^A-Z0-9_]+`)

func sanitizeManifestName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.ReplaceAll(name, "_", "-")
	name = invalidManifestNameRunes.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		return "resource"
	}
	return name
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func writeComposeZarfConfig(opts ComposeOptions, components []composeComponentSpec, variables []v1alpha1.InteractiveVariable) error {
	pkg := v1alpha1.ZarfPackage{
		APIVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.ZarfPackageConfig,
		Metadata: v1alpha1.ZarfMetadata{
			Name:        opts.App.Name,
			Version:     opts.App.Version,
			Description: fmt.Sprintf("Generated by keel from %s", opts.App.ComposeFilePath),
		},
		Variables: variables,
	}

	ordered, err := orderComposeComponents(components)
	if err != nil {
		return err
	}
	for _, svc := range ordered {
		component := v1alpha1.ZarfComponent{
			Name:     svc.Name,
			Required: boolPtr(true),
			Manifests: []v1alpha1.ZarfManifest{
				{
					Name:      svc.Name + "-manifests",
					Namespace: svc.Namespace,
					Files:     svc.ManifestFiles,
				},
			},
		}
		if svc.UsesArchive {
			component.ImageArchives = append(component.ImageArchives, v1alpha1.ImageArchive{
				Path:   svc.ImageArchive,
				Images: []string{svc.Image},
			})
		}
		component.Images = dedupeStrings(svc.Images)
		pkg.Components = append(pkg.Components, component)
	}

	yamlData, err := sigsyaml.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("marshal zarf config: %w", err)
	}
	preface := "# yaml-language-server: $schema=" + zarf.SchemaURL + "\n"
	return os.WriteFile(filepath.Join(opts.Dist.RootPath, "zarf.yaml"), append([]byte(preface), yamlData...), 0o644)
}

func orderComposeComponents(components []composeComponentSpec) ([]composeComponentSpec, error) {
	if len(components) <= 1 {
		return components, nil
	}

	componentByName := map[string]composeComponentSpec{}
	indegree := map[string]int{}
	dependents := map[string][]string{}
	orderedNames := make([]string, 0, len(components))
	for _, component := range components {
		componentByName[component.Name] = component
		indegree[component.Name] = 0
		orderedNames = append(orderedNames, component.Name)
	}

	for _, component := range components {
		for _, dep := range component.DependsOn {
			if _, exists := componentByName[dep]; !exists {
				continue
			}
			indegree[component.Name]++
			dependents[dep] = append(dependents[dep], component.Name)
		}
	}

	sort.Strings(orderedNames)
	queue := make([]string, 0, len(orderedNames))
	for _, name := range orderedNames {
		if indegree[name] == 0 {
			queue = append(queue, name)
		}
	}

	ordered := make([]composeComponentSpec, 0, len(components))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		ordered = append(ordered, componentByName[current])

		next := append([]string(nil), dependents[current]...)
		sort.Strings(next)
		for _, dependent := range next {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(ordered) != len(components) {
		return nil, fmt.Errorf("compose depends_on graph contains a cycle; unable to determine deterministic component order")
	}

	return ordered, nil
}

const composeUDSPackageTemplate = `apiVersion: uds.dev/v1alpha1
kind: Package
metadata:
  name: {{ .Name }}
  namespace: {{ .Namespace }}
spec:
  network:
    serviceMesh:
      mode: ambient
    expose:{{ range .Exposes }}
      - service: {{ .Service }}
        selector:
          app.kubernetes.io/name: {{ .Service }}
        gateway: tenant
        host: {{ .Host }}
        port: {{ .Port }}{{ end }}
    allow:
      - direction: Ingress
        remoteGenerated: IntraNamespace
      - direction: Egress
        remoteGenerated: IntraNamespace
`
