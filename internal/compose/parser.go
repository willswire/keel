package compose

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"

	"github.com/willswire/keel/internal/model"
)

type ParseOptions struct {
	Profiles []string
}

type composeFile struct {
	Name     string                         `yaml:"name"`
	Include  yamlv3.Node                    `yaml:"include"`
	Services map[string]composeService      `yaml:"services"`
	Volumes  map[string]composeTopVolumeDef `yaml:"volumes"`
	Secrets  map[string]composeTopSecretDef `yaml:"secrets"`
}

type composeService struct {
	Image       string             `yaml:"image"`
	Build       composeBuild       `yaml:"build"`
	Ports       []composePort      `yaml:"ports"`
	Expose      []string           `yaml:"expose"`
	Environment yamlv3.Node        `yaml:"environment"`
	EnvFile     yamlv3.Node        `yaml:"env_file"`
	Command     yamlv3.Node        `yaml:"command"`
	Entrypoint  yamlv3.Node        `yaml:"entrypoint"`
	User        string             `yaml:"user"`
	Healthcheck composeHealthcheck `yaml:"healthcheck"`
	Volumes     []composeVolumeRef `yaml:"volumes"`
	DependsOn   yamlv3.Node        `yaml:"depends_on"`
	Profiles    []string           `yaml:"profiles"`
	Secrets     yamlv3.Node        `yaml:"secrets"`
	Deploy      composeDeploy      `yaml:"deploy"`
}

type composeHealthcheck struct {
	Test yamlv3.Node `yaml:"test"`
}

type composeBuild struct {
	Present    bool
	Context    string
	Dockerfile string
}

func (b *composeBuild) UnmarshalYAML(value *yamlv3.Node) error {
	switch value.Kind {
	case 0, yamlv3.ScalarNode:
		if strings.TrimSpace(value.Value) == "" || strings.EqualFold(strings.TrimSpace(value.Value), "null") {
			return nil
		}
		b.Present = true
		b.Context = strings.TrimSpace(value.Value)
		b.Dockerfile = "Dockerfile"
		return nil
	case yamlv3.MappingNode:
		var raw struct {
			Context    string `yaml:"context"`
			Dockerfile string `yaml:"dockerfile"`
		}
		if err := value.Decode(&raw); err != nil {
			return fmt.Errorf("parse build config: %w", err)
		}
		b.Present = true
		b.Context = strings.TrimSpace(raw.Context)
		if b.Context == "" {
			b.Context = "."
		}
		b.Dockerfile = strings.TrimSpace(raw.Dockerfile)
		if b.Dockerfile == "" {
			b.Dockerfile = "Dockerfile"
		}
		return nil
	default:
		return fmt.Errorf("build must be a string path or object")
	}
}

type composePort struct {
	Target   int
	Protocol string
}

func (p *composePort) UnmarshalYAML(value *yamlv3.Node) error {
	switch value.Kind {
	case yamlv3.ScalarNode:
		target, proto, err := parsePortToken(value.Value)
		if err != nil {
			return err
		}
		p.Target = target
		p.Protocol = proto
		return nil
	case yamlv3.MappingNode:
		var raw struct {
			Target   int    `yaml:"target"`
			Protocol string `yaml:"protocol"`
		}
		if err := value.Decode(&raw); err != nil {
			return fmt.Errorf("parse ports entry: %w", err)
		}
		if raw.Target <= 0 {
			return fmt.Errorf("ports.target must be set and greater than 0")
		}
		p.Target = raw.Target
		p.Protocol = protocolOrDefault(raw.Protocol)
		return nil
	default:
		return fmt.Errorf("ports entry must be string or object")
	}
}

type composeExternal struct {
	Enabled bool
}

func (e *composeExternal) UnmarshalYAML(value *yamlv3.Node) error {
	switch value.Kind {
	case 0:
		return nil
	case yamlv3.ScalarNode:
		trimmed := strings.TrimSpace(strings.ToLower(value.Value))
		if trimmed == "" || trimmed == "null" {
			return nil
		}
		if trimmed == "true" {
			e.Enabled = true
			return nil
		}
		if trimmed == "false" {
			e.Enabled = false
			return nil
		}
		return fmt.Errorf("external must be a boolean or mapping")
	case yamlv3.MappingNode:
		e.Enabled = true
		return nil
	default:
		return fmt.Errorf("external must be a boolean or mapping")
	}
}

type composeTopVolumeDef struct {
	Name     string          `yaml:"name"`
	External composeExternal `yaml:"external"`
}

func (v *composeTopVolumeDef) UnmarshalYAML(value *yamlv3.Node) error {
	switch value.Kind {
	case 0:
		return nil
	case yamlv3.MappingNode:
		var raw struct {
			Name     string          `yaml:"name"`
			External composeExternal `yaml:"external"`
		}
		if err := value.Decode(&raw); err != nil {
			return fmt.Errorf("parse top-level volume: %w", err)
		}
		v.Name = strings.TrimSpace(raw.Name)
		v.External = raw.External
		return nil
	case yamlv3.ScalarNode:
		if strings.TrimSpace(value.Value) == "" || strings.EqualFold(strings.TrimSpace(value.Value), "null") {
			return nil
		}
		return fmt.Errorf("top-level volumes entries must be mappings")
	default:
		return fmt.Errorf("top-level volumes entries must be mappings")
	}
}

type composeTopSecretDef struct {
	Name        string          `yaml:"name"`
	External    composeExternal `yaml:"external"`
	File        string          `yaml:"file"`
	Environment string          `yaml:"environment"`
}

func (s *composeTopSecretDef) UnmarshalYAML(value *yamlv3.Node) error {
	switch value.Kind {
	case 0:
		return nil
	case yamlv3.MappingNode:
		var raw struct {
			Name        string          `yaml:"name"`
			External    composeExternal `yaml:"external"`
			File        string          `yaml:"file"`
			Environment string          `yaml:"environment"`
		}
		if err := value.Decode(&raw); err != nil {
			return fmt.Errorf("parse top-level secret: %w", err)
		}
		s.Name = strings.TrimSpace(raw.Name)
		s.External = raw.External
		s.File = strings.TrimSpace(raw.File)
		s.Environment = strings.TrimSpace(raw.Environment)
		return nil
	case yamlv3.ScalarNode:
		if strings.TrimSpace(value.Value) == "" || strings.EqualFold(strings.TrimSpace(value.Value), "null") {
			return nil
		}
		return fmt.Errorf("top-level secrets entries must be mappings")
	default:
		return fmt.Errorf("top-level secrets entries must be mappings")
	}
}

type composeVolumeRef struct {
	Type     string
	Source   string
	Target   string
	ReadOnly bool
}

func (v *composeVolumeRef) UnmarshalYAML(value *yamlv3.Node) error {
	switch value.Kind {
	case yamlv3.ScalarNode:
		parsed, err := parseVolumeToken(value.Value)
		if err != nil {
			return err
		}
		*v = parsed
		return nil
	case yamlv3.MappingNode:
		var raw struct {
			Type     string `yaml:"type"`
			Source   string `yaml:"source"`
			Target   string `yaml:"target"`
			ReadOnly bool   `yaml:"read_only"`
		}
		if err := value.Decode(&raw); err != nil {
			return fmt.Errorf("parse volume entry: %w", err)
		}
		v.Type = strings.ToLower(strings.TrimSpace(raw.Type))
		v.Source = strings.TrimSpace(raw.Source)
		v.Target = strings.TrimSpace(raw.Target)
		v.ReadOnly = raw.ReadOnly
		if v.Type == "" {
			if looksLikePath(v.Source) {
				v.Type = "bind"
			} else {
				v.Type = "volume"
			}
		}
		if v.Type != "volume" && v.Type != "bind" {
			return fmt.Errorf("unsupported volume type %q (supported: volume, bind)", v.Type)
		}
		if v.Target == "" {
			return fmt.Errorf("volume.target must be set")
		}
		if v.Type == "bind" && v.Source == "" {
			return fmt.Errorf("bind volume must define source")
		}
		return nil
	default:
		return fmt.Errorf("volumes entry must be string or object")
	}
}

type composeDeploy struct {
	Resources composeDeployResources `yaml:"resources"`
}

type composeDeployResources struct {
	Limits       composeDeployResourceValues `yaml:"limits"`
	Reservations composeDeployResourceValues `yaml:"reservations"`
}

type composeDeployResourceValues struct {
	CPUs   string `yaml:"cpus"`
	Memory string `yaml:"memory"`
}

// ParseFile parses a compose file into a normalized spec.
func ParseFile(path string) (model.ComposeAppSpec, error) {
	return ParseFileWithOptions(path, ParseOptions{})
}

// ParseFileWithOptions parses a compose file using caller-provided options.
func ParseFileWithOptions(path string, opts ParseOptions) (model.ComposeAppSpec, error) {
	composeAbs, err := filepath.Abs(path)
	if err != nil {
		return model.ComposeAppSpec{}, fmt.Errorf("resolve compose file path: %w", err)
	}
	composeDir := filepath.Dir(composeAbs)

	file, err := loadComposeFile(composeAbs, nil)
	if err != nil {
		return model.ComposeAppSpec{}, err
	}
	if len(file.Services) == 0 {
		return model.ComposeAppSpec{}, fmt.Errorf("compose file %s must define at least one service", path)
	}

	projectName := strings.TrimSpace(file.Name)
	if projectName == "" {
		projectName = defaultProjectNameFromComposePath(composeAbs)
	}
	projectName, err = normalizeName(projectName)
	if err != nil {
		return model.ComposeAppSpec{}, fmt.Errorf("invalid compose project name: %w", err)
	}

	volumes, volumeAliases, err := normalizeTopLevelVolumes(file.Volumes)
	if err != nil {
		return model.ComposeAppSpec{}, err
	}
	secrets, secretAliases, err := normalizeTopLevelSecrets(file.Secrets, composeDir)
	if err != nil {
		return model.ComposeAppSpec{}, err
	}

	keys := make([]string, 0, len(file.Services))
	for k := range file.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	type serviceEntry struct {
		rawKey string
		raw    composeService
		name   string
	}
	entries := make(map[string]serviceEntry, len(keys))
	serviceAliases := map[string]string{}
	for _, key := range keys {
		name, err := normalizeName(key)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("invalid service name %q: %w", key, err)
		}
		if _, exists := entries[name]; exists {
			return model.ComposeAppSpec{}, fmt.Errorf("duplicate normalized service name %q", name)
		}
		entry := serviceEntry{rawKey: key, raw: file.Services[key], name: name}
		entries[name] = entry
		registerAlias(serviceAliases, key, name)
		registerAlias(serviceAliases, name, name)
	}

	activeProfiles := makeProfileSet(opts.Profiles)
	selected := map[string]struct{}{}
	for _, entry := range entries {
		profiles := normalizeProfiles(entry.raw.Profiles)
		if includeService(profiles, activeProfiles) {
			selected[entry.name] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return model.ComposeAppSpec{}, fmt.Errorf("no compose services selected; services with profiles require --compose-profile")
	}

	for {
		changed := false
		for name := range selected {
			entry := entries[name]
			deps, err := parseDependsOn(entry.raw.DependsOn, serviceAliases)
			if err != nil {
				return model.ComposeAppSpec{}, fmt.Errorf("service %q depends_on: %w", entry.rawKey, err)
			}
			for _, dep := range deps {
				if _, ok := selected[dep.Service]; !ok {
					selected[dep.Service] = struct{}{}
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	selectedNames := make([]string, 0, len(selected))
	for name := range selected {
		selectedNames = append(selectedNames, name)
	}
	sort.Strings(selectedNames)

	services := make([]model.ComposeServiceSpec, 0, len(selectedNames))
	for _, selectedName := range selectedNames {
		entry := entries[selectedName]
		rawSvc := entry.raw

		image := strings.TrimSpace(rawSvc.Image)
		buildSpec, err := resolveBuild(rawSvc.Build, composeDir)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q build config: %w", entry.rawKey, err)
		}
		if image == "" && buildSpec == nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q must define either image or build", entry.rawKey)
		}
		if image == "" {
			image = fmt.Sprintf("keel.local/%s:latest", selectedName)
		}

		envFromFiles, err := parseEnvFiles(rawSvc.EnvFile, composeDir)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q env_file: %w", entry.rawKey, err)
		}
		envInline, err := parseEnvironment(rawSvc.Environment)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q environment: %w", entry.rawKey, err)
		}
		env := mergeEnv(envFromFiles, envInline)

		command, err := parseCommandLike(rawSvc.Command)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q command: %w", entry.rawKey, err)
		}
		entrypoint, err := parseCommandLike(rawSvc.Entrypoint)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q entrypoint: %w", entry.rawKey, err)
		}
		healthcheck, err := parseHealthcheck(rawSvc.Healthcheck)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q healthcheck.test: %w", entry.rawKey, err)
		}
		ports, err := parsePorts(rawSvc.Ports, rawSvc.Expose)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q ports: %w", entry.rawKey, err)
		}
		volumeMounts, err := parseServiceVolumes(rawSvc.Volumes, selectedName, composeDir, volumeAliases, volumes)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q volumes: %w", entry.rawKey, err)
		}
		secretRefs, err := parseServiceSecrets(rawSvc.Secrets, secretAliases)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q secrets: %w", entry.rawKey, err)
		}
		dependsOn, err := parseDependsOn(rawSvc.DependsOn, serviceAliases)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q depends_on: %w", entry.rawKey, err)
		}

		services = append(services, model.ComposeServiceSpec{
			Name:      selectedName,
			Namespace: projectName,
			Image:     image,
			Build:     buildSpec,
			Volumes:   volumeMounts,
			Secrets:   secretRefs,
			DependsOn: dependsOn,
			Profiles:  normalizeProfiles(rawSvc.Profiles),
			Resources: parseResources(rawSvc.Deploy),
			Dockerfile: model.DockerfileSpec{
				Name:         selectedName,
				ExposedPorts: ports,
				Env:          env,
				User:         strings.TrimSpace(rawSvc.User),
				Cmd:          command,
				Entrypoint:   entrypoint,
				Healthcheck:  healthcheck,
			},
		})
	}

	return model.ComposeAppSpec{
		Name:            projectName,
		Namespace:       projectName,
		ComposeFilePath: composeAbs,
		Services:        services,
		Volumes:         volumes,
		Secrets:         secrets,
	}, nil
}

func loadComposeFile(path string, stack []string) (composeFile, error) {
	absPath := filepath.Clean(path)
	if !filepath.IsAbs(absPath) {
		resolved, err := filepath.Abs(absPath)
		if err != nil {
			return composeFile{}, fmt.Errorf("resolve compose file path %s: %w", path, err)
		}
		absPath = resolved
	}
	for _, parent := range stack {
		if parent == absPath {
			return composeFile{}, fmt.Errorf("compose include cycle detected at %s", absPath)
		}
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return composeFile{}, fmt.Errorf("read compose file %s: %w", absPath, err)
	}

	var current composeFile
	if err := yamlv3.Unmarshal(content, &current); err != nil {
		return composeFile{}, fmt.Errorf("parse compose file %s: %w", absPath, err)
	}

	baseDir := filepath.Dir(absPath)
	includePaths, err := parseIncludePaths(current.Include, baseDir)
	if err != nil {
		return composeFile{}, fmt.Errorf("parse include in %s: %w", absPath, err)
	}

	merged := composeFile{
		Services: map[string]composeService{},
		Volumes:  map[string]composeTopVolumeDef{},
		Secrets:  map[string]composeTopSecretDef{},
	}
	for _, includePath := range includePaths {
		child, err := loadComposeFile(includePath, append(stack, absPath))
		if err != nil {
			return composeFile{}, err
		}
		merged = mergeComposeFiles(merged, child)
	}
	merged = mergeComposeFiles(merged, current)
	return merged, nil
}

func mergeComposeFiles(base composeFile, overlay composeFile) composeFile {
	if base.Services == nil {
		base.Services = map[string]composeService{}
	}
	if base.Volumes == nil {
		base.Volumes = map[string]composeTopVolumeDef{}
	}
	if base.Secrets == nil {
		base.Secrets = map[string]composeTopSecretDef{}
	}

	if strings.TrimSpace(overlay.Name) != "" {
		base.Name = strings.TrimSpace(overlay.Name)
	}
	for key, value := range overlay.Services {
		base.Services[key] = value
	}
	for key, value := range overlay.Volumes {
		base.Volumes[key] = value
	}
	for key, value := range overlay.Secrets {
		base.Secrets[key] = value
	}
	return base
}

func parseIncludePaths(node yamlv3.Node, baseDir string) ([]string, error) {
	switch node.Kind {
	case 0:
		return nil, nil
	case yamlv3.ScalarNode:
		return []string{resolveRelativePath(baseDir, node.Value)}, nil
	case yamlv3.SequenceNode:
		paths := []string{}
		for _, item := range node.Content {
			resolved, err := parseIncludePaths(*item, baseDir)
			if err != nil {
				return nil, err
			}
			paths = append(paths, resolved...)
		}
		return paths, nil
	case yamlv3.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := strings.TrimSpace(node.Content[i].Value)
			if key != "path" {
				continue
			}
			return parseIncludePaths(*node.Content[i+1], baseDir)
		}
		return nil, fmt.Errorf("include mapping must define path")
	default:
		return nil, fmt.Errorf("include must be string, sequence, or mapping")
	}
}

func resolveRelativePath(baseDir string, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Clean(filepath.Join(home, strings.TrimPrefix(trimmed, "~/")))
		}
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	return filepath.Clean(filepath.Join(baseDir, trimmed))
}

func makeProfileSet(values []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out[trimmed] = struct{}{}
	}
	return out
}

func normalizeProfiles(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, profile := range raw {
		trimmed := strings.TrimSpace(profile)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func includeService(serviceProfiles []string, activeProfiles map[string]struct{}) bool {
	if len(serviceProfiles) == 0 {
		return true
	}
	if len(activeProfiles) == 0 {
		return false
	}
	for _, profile := range serviceProfiles {
		if _, ok := activeProfiles[profile]; ok {
			return true
		}
	}
	return false
}

func resolveBuild(raw composeBuild, composeDir string) (*model.ComposeBuildSpec, error) {
	if !raw.Present {
		return nil, nil
	}
	if strings.TrimSpace(raw.Context) == "" {
		return nil, fmt.Errorf("build.context cannot be empty")
	}
	contextPath := filepath.Clean(raw.Context)
	dockerfilePath := filepath.Clean(raw.Dockerfile)
	if !filepath.IsAbs(contextPath) {
		contextPath = filepath.Join(composeDir, contextPath)
	}
	if !filepath.IsAbs(dockerfilePath) {
		dockerfilePath = filepath.Join(contextPath, dockerfilePath)
	}
	if strings.EqualFold(filepath.Base(dockerfilePath), "Dockerfile") && !fileExists(dockerfilePath) {
		containerfilePath := filepath.Join(contextPath, "Containerfile")
		if fileExists(containerfilePath) {
			dockerfilePath = containerfilePath
		}
	}
	return &model.ComposeBuildSpec{
		ContextPath:    contextPath,
		DockerfilePath: dockerfilePath,
	}, nil
}

func fileExists(path string) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

func parseEnvFiles(node yamlv3.Node, composeDir string) ([]model.EnvVar, error) {
	type envRef struct {
		Path     string
		Required bool
	}

	refs := []envRef{}
	switch node.Kind {
	case 0:
		return nil, nil
	case yamlv3.ScalarNode:
		path := strings.TrimSpace(node.Value)
		if path == "" {
			return nil, nil
		}
		refs = append(refs, envRef{Path: path, Required: true})
	case yamlv3.MappingNode:
		var raw struct {
			Path     string `yaml:"path"`
			Required *bool  `yaml:"required"`
		}
		if err := node.Decode(&raw); err != nil {
			return nil, fmt.Errorf("parse env_file object: %w", err)
		}
		required := true
		if raw.Required != nil {
			required = *raw.Required
		}
		refs = append(refs, envRef{Path: strings.TrimSpace(raw.Path), Required: required})
	case yamlv3.SequenceNode:
		for _, item := range node.Content {
			switch item.Kind {
			case yamlv3.ScalarNode:
				path := strings.TrimSpace(item.Value)
				if path == "" {
					continue
				}
				refs = append(refs, envRef{Path: path, Required: true})
			case yamlv3.MappingNode:
				var raw struct {
					Path     string `yaml:"path"`
					Required *bool  `yaml:"required"`
				}
				if err := item.Decode(&raw); err != nil {
					return nil, fmt.Errorf("parse env_file object: %w", err)
				}
				required := true
				if raw.Required != nil {
					required = *raw.Required
				}
				refs = append(refs, envRef{Path: strings.TrimSpace(raw.Path), Required: required})
			default:
				return nil, fmt.Errorf("env_file entries must be string or mapping")
			}
		}
	default:
		return nil, fmt.Errorf("env_file must be string, list, or mapping")
	}

	merged := map[string]string{}
	for _, ref := range refs {
		if ref.Path == "" {
			continue
		}
		resolved := resolveRelativePath(composeDir, ref.Path)
		vars, err := readEnvFile(resolved)
		if err != nil {
			if os.IsNotExist(err) && !ref.Required {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", resolved, err)
		}
		for key, value := range vars {
			merged[key] = value
		}
	}

	if len(merged) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]model.EnvVar, 0, len(names))
	for _, name := range names {
		out = append(out, model.EnvVar{Name: name, Value: merged[name]})
	}
	return out, nil
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		parts := strings.SplitN(line, "=", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		value := ""
		if len(parts) == 2 {
			value = strings.TrimSpace(parts[1])
			value = strings.Trim(value, `"`)
			value = strings.Trim(value, `'`)
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseEnvironment(node yamlv3.Node) ([]model.EnvVar, error) {
	switch node.Kind {
	case 0:
		return nil, nil
	case yamlv3.MappingNode:
		ordered := make([]model.EnvVar, 0, len(node.Content)/2)
		indexByName := map[string]int{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			name := strings.TrimSpace(node.Content[i].Value)
			value := strings.TrimSpace(node.Content[i+1].Value)
			if name == "" {
				continue
			}
			if pos, ok := indexByName[name]; ok {
				ordered[pos] = model.EnvVar{Name: name, Value: value}
				continue
			}
			indexByName[name] = len(ordered)
			ordered = append(ordered, model.EnvVar{Name: name, Value: value})
		}
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
		return ordered, nil
	case yamlv3.SequenceNode:
		ordered := []model.EnvVar{}
		indexByName := map[string]int{}
		for _, item := range node.Content {
			token := strings.TrimSpace(item.Value)
			if token == "" {
				continue
			}
			parts := strings.SplitN(token, "=", 2)
			name := strings.TrimSpace(parts[0])
			if name == "" {
				continue
			}
			value := ""
			if len(parts) == 2 {
				value = strings.TrimSpace(parts[1])
			}
			if pos, ok := indexByName[name]; ok {
				ordered[pos] = model.EnvVar{Name: name, Value: value}
				continue
			}
			indexByName[name] = len(ordered)
			ordered = append(ordered, model.EnvVar{Name: name, Value: value})
		}
		return ordered, nil
	default:
		return nil, fmt.Errorf("environment must be a mapping or list")
	}
}

func mergeEnv(base []model.EnvVar, override []model.EnvVar) []model.EnvVar {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := map[string]string{}
	for _, item := range base {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		merged[name] = item.Value
	}
	for _, item := range override {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		merged[name] = item.Value
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]model.EnvVar, 0, len(names))
	for _, name := range names {
		out = append(out, model.EnvVar{Name: name, Value: merged[name]})
	}
	return out
}

func parseCommandLike(node yamlv3.Node) (string, error) {
	switch node.Kind {
	case 0:
		return "", nil
	case yamlv3.ScalarNode:
		return strings.TrimSpace(node.Value), nil
	case yamlv3.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			out = append(out, item.Value)
		}
		buf, err := json.Marshal(out)
		if err != nil {
			return "", err
		}
		return string(buf), nil
	default:
		return "", fmt.Errorf("must be string or list")
	}
}

func parseHealthcheck(raw composeHealthcheck) (string, error) {
	node := raw.Test
	switch node.Kind {
	case 0:
		return "", nil
	case yamlv3.ScalarNode:
		value := strings.TrimSpace(node.Value)
		if value == "" {
			return "", nil
		}
		if strings.EqualFold(value, "none") {
			return "NONE", nil
		}
		return "CMD-SHELL " + value, nil
	case yamlv3.SequenceNode:
		items := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			items = append(items, strings.TrimSpace(item.Value))
		}
		if len(items) == 0 {
			return "", nil
		}
		mode := strings.ToUpper(items[0])
		switch mode {
		case "NONE":
			return "NONE", nil
		case "CMD":
			buf, err := json.Marshal(items[1:])
			if err != nil {
				return "", err
			}
			return "CMD " + string(buf), nil
		case "CMD-SHELL":
			if len(items) == 1 {
				return "", nil
			}
			return "CMD-SHELL " + strings.Join(items[1:], " "), nil
		default:
			buf, err := json.Marshal(items)
			if err != nil {
				return "", err
			}
			return "CMD " + string(buf), nil
		}
	default:
		return "", fmt.Errorf("must be string or list")
	}
}

func parsePorts(rawPorts []composePort, expose []string) ([]model.Port, error) {
	ports := make([]model.Port, 0, len(rawPorts)+len(expose))
	seen := map[string]struct{}{}

	addPort := func(num int, proto string, raw string) {
		if num <= 0 {
			return
		}
		key := fmt.Sprintf("%d/%s", num, strings.ToUpper(proto))
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		ports = append(ports, model.Port{
			Number:   num,
			Protocol: strings.ToUpper(proto),
			Raw:      raw,
		})
	}

	for _, p := range rawPorts {
		addPort(p.Target, protocolOrDefault(p.Protocol), fmt.Sprintf("%d/%s", p.Target, strings.ToUpper(protocolOrDefault(p.Protocol))))
	}
	for _, token := range expose {
		num, proto, err := parsePortToken(token)
		if err != nil {
			return nil, err
		}
		addPort(num, proto, token)
	}

	sort.SliceStable(ports, func(i, j int) bool {
		if ports[i].Number == ports[j].Number {
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].Number < ports[j].Number
	})
	return ports, nil
}

func parsePortToken(token string) (int, string, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return 0, "", fmt.Errorf("port token cannot be empty")
	}

	proto := "TCP"
	if strings.Contains(trimmed, "/") {
		parts := strings.SplitN(trimmed, "/", 2)
		trimmed = strings.TrimSpace(parts[0])
		proto = protocolOrDefault(parts[1])
	}

	segment := trimmed
	if strings.Contains(segment, ":") {
		parts := strings.Split(segment, ":")
		segment = parts[len(parts)-1]
	}
	if strings.Contains(segment, "-") {
		segment = strings.SplitN(segment, "-", 2)[0]
	}

	n, err := strconv.Atoi(strings.TrimSpace(segment))
	if err != nil || n <= 0 {
		return 0, "", fmt.Errorf("invalid port token %q", token)
	}
	return n, proto, nil
}

func protocolOrDefault(raw string) string {
	proto := strings.ToUpper(strings.TrimSpace(raw))
	if proto == "" {
		return "TCP"
	}
	return proto
}

func parseVolumeToken(token string) (composeVolumeRef, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return composeVolumeRef{}, fmt.Errorf("volume token cannot be empty")
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) == 1 {
		return composeVolumeRef{Type: "volume", Target: strings.TrimSpace(parts[0])}, nil
	}

	mainParts := parts
	options := ""
	last := strings.TrimSpace(parts[len(parts)-1])
	if isVolumeOptionToken(last) {
		options = last
		mainParts = parts[:len(parts)-1]
	}
	if len(mainParts) < 2 {
		return composeVolumeRef{}, fmt.Errorf("invalid volume token %q", token)
	}
	source := strings.Join(mainParts[:len(mainParts)-1], ":")
	target := strings.TrimSpace(mainParts[len(mainParts)-1])
	if target == "" {
		return composeVolumeRef{}, fmt.Errorf("invalid volume token %q", token)
	}

	kind := "volume"
	if looksLikePath(source) {
		kind = "bind"
	}

	readOnly := false
	if options != "" {
		for _, opt := range strings.Split(options, ",") {
			if strings.EqualFold(strings.TrimSpace(opt), "ro") {
				readOnly = true
			}
		}
	}

	return composeVolumeRef{
		Type:     kind,
		Source:   strings.TrimSpace(source),
		Target:   target,
		ReadOnly: readOnly,
	}, nil
}

func isVolumeOptionToken(token string) bool {
	if token == "" {
		return false
	}
	options := strings.Split(token, ",")
	for _, opt := range options {
		trimmed := strings.ToLower(strings.TrimSpace(opt))
		if trimmed == "" {
			return false
		}
		switch trimmed {
		case "ro", "rw", "z", "delegated", "cached", "consistent", "nocopy":
			continue
		default:
			if strings.Contains(trimmed, "=") {
				continue
			}
			return false
		}
	}
	return true
}

func looksLikePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "~/") {
		return true
	}
	if windowsPathRegexp.MatchString(trimmed) {
		return true
	}
	return false
}

func parseServiceVolumes(raw []composeVolumeRef, serviceName string, composeDir string, aliases map[string]string, volumes map[string]model.ComposeVolumeSpec) ([]model.ComposeVolumeMount, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]model.ComposeVolumeMount, 0, len(raw))
	for idx, mount := range raw {
		target := strings.TrimSpace(mount.Target)
		if target == "" {
			return nil, fmt.Errorf("volume entry %d is missing target", idx)
		}
		kind := strings.ToLower(strings.TrimSpace(mount.Type))
		if kind == "" {
			if looksLikePath(mount.Source) {
				kind = "bind"
			} else {
				kind = "volume"
			}
		}

		switch kind {
		case "volume":
			source := strings.TrimSpace(mount.Source)
			name := ""
			if source == "" {
				name = anonymousVolumeName(serviceName, target)
			} else if alias, ok := resolveAlias(aliases, source); ok {
				name = alias
			} else {
				if looksLikePath(source) {
					return nil, fmt.Errorf("volume source %q looks like a path but type is volume", source)
				}
				normalized, err := normalizeName(source)
				if err != nil {
					return nil, fmt.Errorf("invalid volume source %q: %w", source, err)
				}
				name = normalized
			}
			if _, exists := volumes[name]; !exists {
				volumes[name] = model.ComposeVolumeSpec{Name: name}
			}
			out = append(out, model.ComposeVolumeMount{
				Name:     name,
				Type:     "volume",
				Source:   source,
				Target:   target,
				ReadOnly: mount.ReadOnly,
			})
		case "bind":
			source := strings.TrimSpace(mount.Source)
			if source == "" {
				return nil, fmt.Errorf("bind volume at target %q must define source", target)
			}
			resolved := resolveRelativePath(composeDir, source)
			out = append(out, model.ComposeVolumeMount{
				Type:       "bind",
				Source:     source,
				SourcePath: resolved,
				Target:     target,
				ReadOnly:   mount.ReadOnly,
			})
		default:
			return nil, fmt.Errorf("unsupported volume type %q", kind)
		}
	}
	return out, nil
}

func anonymousVolumeName(serviceName string, target string) string {
	base := strings.Trim(target, "/")
	base = strings.ReplaceAll(base, "/", "-")
	base = strings.ReplaceAll(base, "_", "-")
	base = invalidNameRunes.ReplaceAllString(strings.ToLower(base), "-")
	base = strings.Trim(base, "-.")
	if base == "" {
		base = "data"
	}
	name, err := normalizeName(serviceName + "-" + base)
	if err != nil {
		return serviceName + "-data"
	}
	return name
}

func normalizeTopLevelVolumes(raw map[string]composeTopVolumeDef) (map[string]model.ComposeVolumeSpec, map[string]string, error) {
	volumes := map[string]model.ComposeVolumeSpec{}
	aliases := map[string]string{}
	if len(raw) == 0 {
		return volumes, aliases, nil
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		def := raw[key]
		targetName := strings.TrimSpace(def.Name)
		if targetName == "" {
			targetName = key
		}
		normalizedTarget, err := normalizeName(targetName)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid volume name %q: %w", targetName, err)
		}
		if existing, ok := volumes[normalizedTarget]; ok {
			if existing.External != def.External.Enabled {
				return nil, nil, fmt.Errorf("conflicting volume definitions for %q", normalizedTarget)
			}
		} else {
			volumes[normalizedTarget] = model.ComposeVolumeSpec{Name: normalizedTarget, External: def.External.Enabled}
		}
		registerAlias(aliases, key, normalizedTarget)
		registerAlias(aliases, targetName, normalizedTarget)
		registerAlias(aliases, normalizedTarget, normalizedTarget)
	}
	return volumes, aliases, nil
}

func normalizeTopLevelSecrets(raw map[string]composeTopSecretDef, composeDir string) (map[string]model.ComposeSecretSpec, map[string]string, error) {
	secrets := map[string]model.ComposeSecretSpec{}
	aliases := map[string]string{}
	if len(raw) == 0 {
		return secrets, aliases, nil
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		def := raw[key]
		targetName := strings.TrimSpace(def.Name)
		if targetName == "" {
			targetName = key
		}
		normalizedTarget, err := normalizeName(targetName)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid secret name %q: %w", targetName, err)
		}
		if def.External.Enabled && (def.File != "" || def.Environment != "") {
			return nil, nil, fmt.Errorf("secret %q cannot set external with file/environment", key)
		}
		if def.File != "" && def.Environment != "" {
			return nil, nil, fmt.Errorf("secret %q cannot set both file and environment", key)
		}
		filePath := ""
		if def.File != "" {
			filePath = resolveRelativePath(composeDir, def.File)
		}
		spec := model.ComposeSecretSpec{
			Name:        normalizedTarget,
			External:    def.External.Enabled,
			FilePath:    filePath,
			Environment: strings.TrimSpace(def.Environment),
		}
		if _, exists := secrets[normalizedTarget]; exists {
			return nil, nil, fmt.Errorf("duplicate normalized secret name %q", normalizedTarget)
		}
		secrets[normalizedTarget] = spec
		registerAlias(aliases, key, normalizedTarget)
		registerAlias(aliases, targetName, normalizedTarget)
		registerAlias(aliases, normalizedTarget, normalizedTarget)
	}
	return secrets, aliases, nil
}

func parseServiceSecrets(node yamlv3.Node, aliases map[string]string) ([]model.ComposeServiceSecretSpec, error) {
	switch node.Kind {
	case 0:
		return nil, nil
	case yamlv3.SequenceNode:
		out := make([]model.ComposeServiceSecretSpec, 0, len(node.Content))
		for _, item := range node.Content {
			source := ""
			target := ""
			switch item.Kind {
			case yamlv3.ScalarNode:
				source = strings.TrimSpace(item.Value)
				target = source
			case yamlv3.MappingNode:
				var raw struct {
					Source string `yaml:"source"`
					Target string `yaml:"target"`
				}
				if err := item.Decode(&raw); err != nil {
					return nil, fmt.Errorf("parse secret mapping: %w", err)
				}
				source = strings.TrimSpace(raw.Source)
				target = strings.TrimSpace(raw.Target)
				if target == "" {
					target = source
				}
			default:
				return nil, fmt.Errorf("secrets entries must be string or mapping")
			}
			if source == "" {
				return nil, fmt.Errorf("secret source cannot be empty")
			}
			normalized, ok := resolveAlias(aliases, source)
			if !ok {
				return nil, fmt.Errorf("secret %q is not defined at top-level", source)
			}
			if target == "" {
				target = normalized
			}
			out = append(out, model.ComposeServiceSecretSpec{Source: normalized, Target: target})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("secrets must be a list")
	}
}

func parseDependsOn(node yamlv3.Node, serviceAliases map[string]string) ([]model.ComposeDependencySpec, error) {
	switch node.Kind {
	case 0:
		return nil, nil
	case yamlv3.SequenceNode:
		out := make([]model.ComposeDependencySpec, 0, len(node.Content))
		seen := map[string]struct{}{}
		for _, item := range node.Content {
			name := strings.TrimSpace(item.Value)
			if name == "" {
				continue
			}
			normalized, ok := resolveAlias(serviceAliases, name)
			if !ok {
				return nil, fmt.Errorf("unknown dependency service %q", name)
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			out = append(out, model.ComposeDependencySpec{Service: normalized, Condition: "service_started"})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Service < out[j].Service })
		return out, nil
	case yamlv3.MappingNode:
		keys := make([]string, 0, len(node.Content)/2)
		values := map[string]*yamlv3.Node{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := strings.TrimSpace(node.Content[i].Value)
			keys = append(keys, key)
			values[key] = node.Content[i+1]
		}
		sort.Strings(keys)

		out := make([]model.ComposeDependencySpec, 0, len(keys))
		seen := map[string]struct{}{}
		for _, rawService := range keys {
			normalized, ok := resolveAlias(serviceAliases, rawService)
			if !ok {
				return nil, fmt.Errorf("unknown dependency service %q", rawService)
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			condition := "service_started"
			value := values[rawService]
			if value != nil && value.Kind == yamlv3.MappingNode {
				var raw struct {
					Condition string `yaml:"condition"`
				}
				if err := value.Decode(&raw); err != nil {
					return nil, fmt.Errorf("parse depends_on for service %q: %w", rawService, err)
				}
				if strings.TrimSpace(raw.Condition) != "" {
					condition = strings.TrimSpace(raw.Condition)
				}
			}
			seen[normalized] = struct{}{}
			out = append(out, model.ComposeDependencySpec{Service: normalized, Condition: condition})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("depends_on must be list or mapping")
	}
}

func parseResources(raw composeDeploy) model.ComposeResourcesSpec {
	return model.ComposeResourcesSpec{
		Limits: model.ComposeResourceSet{
			CPU:    strings.TrimSpace(raw.Resources.Limits.CPUs),
			Memory: strings.TrimSpace(raw.Resources.Limits.Memory),
		},
		Requests: model.ComposeResourceSet{
			CPU:    strings.TrimSpace(raw.Resources.Reservations.CPUs),
			Memory: strings.TrimSpace(raw.Resources.Reservations.Memory),
		},
	}
}

func registerAlias(aliases map[string]string, raw string, normalized string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return
	}
	aliases[trimmed] = normalized
	aliases[strings.ToLower(trimmed)] = normalized
	if candidate, err := normalizeName(trimmed); err == nil {
		aliases[candidate] = normalized
	}
}

func resolveAlias(aliases map[string]string, raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if value, ok := aliases[trimmed]; ok {
		return value, true
	}
	if value, ok := aliases[strings.ToLower(trimmed)]; ok {
		return value, true
	}
	candidate, err := normalizeName(trimmed)
	if err == nil {
		if value, ok := aliases[candidate]; ok {
			return value, true
		}
	}
	return "", false
}

var invalidNameRunes = regexp.MustCompile(`[^a-z0-9.-]+`)
var windowsPathRegexp = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func normalizeName(name string) (string, error) {
	out := strings.ToLower(strings.TrimSpace(name))
	out = strings.ReplaceAll(out, "_", "-")
	out = invalidNameRunes.ReplaceAllString(out, "-")
	out = strings.Trim(out, "-.")
	if out == "" {
		return "", fmt.Errorf("name must contain at least one alphanumeric character")
	}
	return out, nil
}

func defaultProjectNameFromComposePath(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSuffix(base, ".compose")
	return base
}
