package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/willswire/keel/internal/model"
)

type ParseOptions struct {
	Profiles []string
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

	project, err := loadProject(composeAbs, opts.Profiles)
	if err != nil {
		return model.ComposeAppSpec{}, err
	}
	if len(project.Services) == 0 {
		return model.ComposeAppSpec{}, fmt.Errorf("no compose services selected; services with profiles require --compose-profile")
	}

	projectNameRaw := strings.TrimSpace(project.Name)
	if projectNameRaw == "" {
		projectNameRaw = defaultProjectNameFromComposePath(composeAbs)
	} else if !composeDeclaresName(composeAbs) {
		dirName, err := normalizeName(filepath.Base(composeDir))
		if err == nil {
			normalizedProject, err := normalizeName(projectNameRaw)
			if err == nil && normalizedProject == dirName {
				projectNameRaw = defaultProjectNameFromComposePath(composeAbs)
			}
		}
	}
	projectName, err := normalizeName(projectNameRaw)
	if err != nil {
		return model.ComposeAppSpec{}, fmt.Errorf("invalid compose project name: %w", err)
	}

	volumes, volumeAliases, err := normalizeTopLevelVolumes(project.Volumes)
	if err != nil {
		return model.ComposeAppSpec{}, err
	}
	secrets, secretAliases, err := normalizeTopLevelSecrets(project.Secrets, composeDir)
	if err != nil {
		return model.ComposeAppSpec{}, err
	}

	keys := make([]string, 0, len(project.Services))
	for key := range project.Services {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	services := make([]model.ComposeServiceSpec, 0, len(keys))
	serviceAliases := map[string]string{}
	for _, key := range keys {
		normalized, err := normalizeName(key)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("invalid service name %q: %w", key, err)
		}
		registerAlias(serviceAliases, key, normalized)
		registerAlias(serviceAliases, normalized, normalized)
	}

	for _, key := range keys {
		rawSvc := project.Services[key]
		serviceName, _ := resolveAlias(serviceAliases, key)

		image := strings.TrimSpace(rawSvc.Image)
		buildSpec, err := resolveBuild(rawSvc.Build, composeDir)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q build config: %w", key, err)
		}
		if image == "" && buildSpec == nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q must define either image or build", key)
		}
		if image == "" {
			image = fmt.Sprintf("keel.local/%s:latest", serviceName)
		}

		envFromFiles, err := parseEnvFiles(rawSvc.EnvFiles, composeDir)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q env_file: %w", key, err)
		}
		envInline := parseEnvironment(rawSvc.Environment)
		env := mergeEnv(envFromFiles, envInline)

		healthcheck, err := healthcheckToString(rawSvc.HealthCheck)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q healthcheck.test: %w", key, err)
		}
		ports, err := parsePorts(rawSvc.Ports, rawSvc.Expose)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q ports: %w", key, err)
		}
		volumeMounts, err := parseServiceVolumes(rawSvc.Volumes, serviceName, composeDir, volumeAliases, volumes)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q volumes: %w", key, err)
		}
		secretRefs, err := parseServiceSecrets(rawSvc.Secrets, secretAliases)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q secrets: %w", key, err)
		}
		dependsOn, err := parseDependsOn(rawSvc.DependsOn, serviceAliases)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q depends_on: %w", key, err)
		}

		services = append(services, model.ComposeServiceSpec{
			Name:      serviceName,
			Namespace: projectName,
			Image:     image,
			Build:     buildSpec,
			Volumes:   volumeMounts,
			Secrets:   secretRefs,
			DependsOn: dependsOn,
			Profiles:  normalizeProfiles(rawSvc.Profiles),
			Resources: parseResources(rawSvc.Deploy),
			Container: model.ContainerSpec{
				Name:         serviceName,
				ExposedPorts: ports,
				Env:          env,
				User:         strings.TrimSpace(rawSvc.User),
				Cmd:          shellCommandToString(rawSvc.Command),
				Entrypoint:   shellCommandToString(rawSvc.Entrypoint),
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

func loadProject(composePath string, profiles []string) (*types.Project, error) {
	workingDir := filepath.Dir(composePath)
	projectOptions, err := cli.NewProjectOptions(
		[]string{composePath},
		cli.WithOsEnv,
		cli.WithWorkingDirectory(workingDir),
		cli.WithInterpolation(true),
		cli.WithProfiles(profiles),
		cli.WithEnvFiles([]string{}...),
		cli.WithDotEnv,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create compose options: %w", err)
	}
	project, err := cli.ProjectFromOptions(context.Background(), projectOptions)
	if err != nil {
		return nil, fmt.Errorf("unable to load compose file: %w", err)
	}
	return project, nil
}

func composeDeclaresName(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var root map[string]any
	if err := yamlv3.Unmarshal(content, &root); err != nil {
		return false
	}
	nameValue, ok := root["name"]
	if !ok {
		return false
	}
	name, ok := nameValue.(string)
	return ok && strings.TrimSpace(name) != ""
}

func resolveBuild(raw *types.BuildConfig, composeDir string) (*model.ComposeBuildSpec, error) {
	if raw == nil {
		return nil, nil
	}
	contextPath := strings.TrimSpace(raw.Context)
	if contextPath == "" {
		contextPath = "."
	}
	if strings.Contains(contextPath, "://") || strings.HasPrefix(contextPath, "git@") {
		return nil, fmt.Errorf("unsupported non-local build context %q", contextPath)
	}
	contextAbs := resolveRelativePath(composeDir, contextPath)

	buildFile := strings.TrimSpace(raw.Dockerfile)
	if buildFile == "" {
		buildFile = "Dockerfile"
	}
	buildFilePath := resolveRelativePath(contextAbs, buildFile)
	if filepath.Base(buildFile) == "Dockerfile" {
		containerfilePath := resolveRelativePath(contextAbs, "Containerfile")
		if !fileExists(buildFilePath) && fileExists(containerfilePath) {
			buildFilePath = containerfilePath
		}
	}

	return &model.ComposeBuildSpec{
		ContextPath:       contextAbs,
		ContainerfilePath: buildFilePath,
		Target:            strings.TrimSpace(raw.Target),
	}, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func parseEnvFiles(raw []types.EnvFile, composeDir string) ([]model.EnvVar, error) {
	merged := map[string]string{}
	order := []string{}
	for _, entry := range raw {
		path := strings.TrimSpace(entry.Path)
		if path == "" {
			continue
		}
		resolved := resolveRelativePath(composeDir, path)
		values, err := readEnvFile(resolved)
		if err != nil {
			required := bool(entry.Required)
			if os.IsNotExist(err) && !required {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", resolved, err)
		}
		for k, v := range values {
			if _, exists := merged[k]; !exists {
				order = append(order, k)
			}
			merged[k] = v
		}
	}

	env := make([]model.EnvVar, 0, len(order))
	for _, key := range order {
		env = append(env, model.EnvVar{Name: key, Value: merged[key]})
	}
	return env, nil
}

func readEnvFile(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(parts[1])
	}
	return out, nil
}

func parseEnvironment(raw types.MappingWithEquals) []model.EnvVar {
	if len(raw) == 0 {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]model.EnvVar, 0, len(keys))
	for _, key := range keys {
		value := ""
		if raw[key] != nil {
			value = *raw[key]
		}
		env = append(env, model.EnvVar{Name: key, Value: value})
	}
	return env
}

func mergeEnv(base []model.EnvVar, override []model.EnvVar) []model.EnvVar {
	order := []string{}
	values := map[string]string{}
	for _, item := range base {
		if _, exists := values[item.Name]; !exists {
			order = append(order, item.Name)
		}
		values[item.Name] = item.Value
	}
	for _, item := range override {
		if _, exists := values[item.Name]; !exists {
			order = append(order, item.Name)
		}
		values[item.Name] = item.Value
	}
	out := make([]model.EnvVar, 0, len(order))
	for _, key := range order {
		out = append(out, model.EnvVar{Name: key, Value: values[key]})
	}
	return out
}

func shellCommandToString(command types.ShellCommand) string {
	if len(command) == 0 {
		return ""
	}
	payload, err := json.Marshal([]string(command))
	if err != nil {
		return strings.Join(command, " ")
	}
	return string(payload)
}

func healthcheckToString(raw *types.HealthCheckConfig) (string, error) {
	if raw == nil || raw.Disable || len(raw.Test) == 0 {
		return "", nil
	}
	tokens := []string(raw.Test)
	mode := strings.ToUpper(strings.TrimSpace(tokens[0]))
	args := tokens[1:]

	switch mode {
	case "NONE":
		return "NONE", nil
	case "CMD":
		payload, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal CMD healthcheck arguments: %w", err)
		}
		return "CMD " + string(payload), nil
	case "CMD-SHELL":
		return "CMD-SHELL " + strings.TrimSpace(strings.Join(args, " ")), nil
	default:
		payload, err := json.Marshal(tokens)
		if err != nil {
			return "", fmt.Errorf("marshal healthcheck command: %w", err)
		}
		return "CMD " + string(payload), nil
	}
}

func parsePorts(rawPorts []types.ServicePortConfig, expose types.StringOrNumberList) ([]model.Port, error) {
	ports := []model.Port{}
	seen := map[string]struct{}{}
	for _, raw := range rawPorts {
		if raw.Target == 0 {
			continue
		}
		proto := protocolOrDefault(raw.Protocol)
		key := fmt.Sprintf("%d/%s", raw.Target, proto)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, model.Port{Number: int(raw.Target), Protocol: proto, Raw: key})
	}

	for _, token := range expose {
		target, proto, err := parsePortToken(token)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d/%s", target, proto)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, model.Port{Number: target, Protocol: proto, Raw: key})
	}

	sort.Slice(ports, func(i, j int) bool {
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
		return 0, "", fmt.Errorf("port must not be empty")
	}

	proto := "TCP"
	base := trimmed
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		base = strings.TrimSpace(trimmed[:idx])
		proto = protocolOrDefault(trimmed[idx+1:])
	}

	parts := strings.Split(base, ":")
	target := strings.TrimSpace(parts[len(parts)-1])
	if strings.Contains(target, "-") {
		rangeParts := strings.SplitN(target, "-", 2)
		target = strings.TrimSpace(rangeParts[0])
	}
	port, err := strconv.Atoi(target)
	if err != nil || port <= 0 {
		return 0, "", fmt.Errorf("invalid port %q", token)
	}
	return port, proto, nil
}

func protocolOrDefault(raw string) string {
	trimmed := strings.ToUpper(strings.TrimSpace(raw))
	if trimmed == "" {
		return "TCP"
	}
	return trimmed
}

func parseServiceVolumes(raw []types.ServiceVolumeConfig, serviceName string, composeDir string, aliases map[string]string, volumes map[string]model.ComposeVolumeSpec) ([]model.ComposeVolumeMount, error) {
	out := make([]model.ComposeVolumeMount, 0, len(raw))
	for _, mount := range raw {
		mountType := strings.ToLower(strings.TrimSpace(mount.Type))
		source := strings.TrimSpace(mount.Source)
		target := strings.TrimSpace(mount.Target)
		if target == "" {
			return nil, fmt.Errorf("volume.target must be set")
		}
		if mountType == "" {
			if looksLikePath(source) {
				mountType = "bind"
			} else {
				mountType = "volume"
			}
		}

		spec := model.ComposeVolumeMount{
			Type:     mountType,
			Source:   source,
			Target:   target,
			ReadOnly: mount.ReadOnly,
		}

		switch mountType {
		case "bind":
			if source == "" {
				return nil, fmt.Errorf("bind volume must define source")
			}
			spec.SourcePath = resolveRelativePath(composeDir, source)
		case "volume":
			if source == "" {
				spec.Name = anonymousVolumeName(serviceName, target)
				volumes[spec.Name] = model.ComposeVolumeSpec{Name: spec.Name}
				registerAlias(aliases, spec.Name, spec.Name)
			} else {
				resolved, ok := resolveAlias(aliases, source)
				if !ok {
					resolvedName, err := normalizeName(source)
					if err != nil {
						return nil, fmt.Errorf("unknown top-level volume %q", source)
					}
					resolved = resolvedName
				}
				spec.Name = resolved
			}
		default:
			return nil, fmt.Errorf("unsupported volume type %q (supported: volume, bind)", mountType)
		}
		out = append(out, spec)
	}
	return out, nil
}

func looksLikePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "~/") {
		return true
	}
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		return true
	}
	return strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\")
}

func anonymousVolumeName(serviceName string, target string) string {
	base := strings.TrimSpace(target)
	if base == "" {
		base = "data"
	}
	base = strings.TrimPrefix(base, "/")
	base = strings.ReplaceAll(base, "/", "-")
	base = strings.ReplaceAll(base, "_", "-")
	base = strings.ReplaceAll(base, ".", "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "data"
	}
	name, err := normalizeName(fmt.Sprintf("%s-%s", serviceName, base))
	if err != nil {
		return "volume"
	}
	return name
}

func normalizeTopLevelVolumes(raw types.Volumes) (map[string]model.ComposeVolumeSpec, map[string]string, error) {
	volumes := map[string]model.ComposeVolumeSpec{}
	aliases := map[string]string{}
	for key, value := range raw {
		nameRaw := key
		normalized, err := normalizeName(nameRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid top-level volume name %q: %w", key, err)
		}
		if _, exists := volumes[normalized]; exists {
			return nil, nil, fmt.Errorf("duplicate normalized top-level volume name %q", normalized)
		}
		volumes[normalized] = model.ComposeVolumeSpec{
			Name:     normalized,
			External: bool(value.External),
		}
		registerAlias(aliases, key, normalized)
		registerAlias(aliases, normalized, normalized)
		if strings.TrimSpace(value.Name) != "" {
			registerAlias(aliases, value.Name, normalized)
		}
	}
	return volumes, aliases, nil
}

func normalizeTopLevelSecrets(raw types.Secrets, composeDir string) (map[string]model.ComposeSecretSpec, map[string]string, error) {
	secrets := map[string]model.ComposeSecretSpec{}
	aliases := map[string]string{}
	for key, value := range raw {
		nameRaw := key
		normalized, err := normalizeName(nameRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid top-level secret name %q: %w", key, err)
		}
		if _, exists := secrets[normalized]; exists {
			return nil, nil, fmt.Errorf("duplicate normalized top-level secret name %q", normalized)
		}

		filePath := strings.TrimSpace(value.File)
		if filePath != "" {
			filePath = resolveRelativePath(composeDir, filePath)
		}
		env := strings.TrimSpace(value.Environment)

		secrets[normalized] = model.ComposeSecretSpec{
			Name:        normalized,
			External:    bool(value.External),
			FilePath:    filePath,
			Environment: env,
		}
		registerAlias(aliases, key, normalized)
		registerAlias(aliases, normalized, normalized)
		if strings.TrimSpace(value.Name) != "" {
			registerAlias(aliases, value.Name, normalized)
		}
	}
	return secrets, aliases, nil
}

func parseServiceSecrets(raw []types.ServiceSecretConfig, aliases map[string]string) ([]model.ComposeServiceSecretSpec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	secrets := make([]model.ComposeServiceSecretSpec, 0, len(raw))
	seen := map[string]struct{}{}
	for _, entry := range raw {
		sourceRaw := strings.TrimSpace(entry.Source)
		if sourceRaw == "" {
			return nil, fmt.Errorf("secret source must not be empty")
		}
		source, ok := resolveAlias(aliases, sourceRaw)
		if !ok {
			normalized, err := normalizeName(sourceRaw)
			if err != nil {
				return nil, fmt.Errorf("unknown top-level secret %q", sourceRaw)
			}
			source = normalized
		}
		target := strings.TrimSpace(entry.Target)
		key := source + "::" + target
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		secrets = append(secrets, model.ComposeServiceSecretSpec{Source: source, Target: target})
	}
	return secrets, nil
}

func parseDependsOn(raw types.DependsOnConfig, serviceAliases map[string]string) ([]model.ComposeDependencySpec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	deps := make([]model.ComposeDependencySpec, 0, len(raw))
	seen := map[string]struct{}{}
	for rawName, dep := range raw {
		resolved, ok := resolveAlias(serviceAliases, rawName)
		if !ok {
			return nil, fmt.Errorf("unknown service %q", rawName)
		}
		if _, exists := seen[resolved]; exists {
			continue
		}
		seen[resolved] = struct{}{}
		condition := strings.TrimSpace(dep.Condition)
		if condition == "" {
			condition = "service_started"
		}
		deps = append(deps, model.ComposeDependencySpec{Service: resolved, Condition: condition})
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Service < deps[j].Service })
	return deps, nil
}

func parseResources(raw *types.DeployConfig) model.ComposeResourcesSpec {
	if raw == nil {
		return model.ComposeResourcesSpec{}
	}
	var out model.ComposeResourcesSpec
	if raw.Resources.Limits != nil {
		out.Limits.CPU = formatNanoCPUs(raw.Resources.Limits.NanoCPUs)
		out.Limits.Memory = formatUnitBytes(raw.Resources.Limits.MemoryBytes)
	}
	if raw.Resources.Reservations != nil {
		out.Requests.CPU = formatNanoCPUs(raw.Resources.Reservations.NanoCPUs)
		out.Requests.Memory = formatUnitBytes(raw.Resources.Reservations.MemoryBytes)
	}
	return out
}

func formatNanoCPUs(value types.NanoCPUs) string {
	v := value.Value()
	if v <= 0 {
		return ""
	}
	return strconv.FormatFloat(float64(v), 'f', -1, 32)
}

func formatUnitBytes(value types.UnitBytes) string {
	if int64(value) <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(value), 10)
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
	if len(out) == 0 {
		return nil
	}
	return out
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

func registerAlias(aliases map[string]string, raw string, normalized string) {
	trimmedRaw := strings.TrimSpace(raw)
	if trimmedRaw == "" {
		return
	}
	trimmedNormalized := strings.TrimSpace(normalized)
	if trimmedNormalized == "" {
		return
	}
	aliases[trimmedRaw] = trimmedNormalized
}

func resolveAlias(aliases map[string]string, raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if resolved, ok := aliases[trimmed]; ok {
		return resolved, true
	}
	normalized, err := normalizeName(trimmed)
	if err != nil {
		return "", false
	}
	if resolved, ok := aliases[normalized]; ok {
		return resolved, true
	}
	return "", false
}

var validNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

func normalizeName(name string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	if !validNamePattern.MatchString(trimmed) {
		return "", fmt.Errorf("name %q must match %s", name, validNamePattern.String())
	}
	return trimmed, nil
}

func defaultProjectNameFromComposePath(path string) string {
	base := filepath.Base(path)
	suffixes := []string{".compose.yml", ".compose.yaml", ".yml", ".yaml"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(strings.ToLower(base), suffix) {
			base = base[:len(base)-len(suffix)]
			break
		}
	}
	normalized, err := normalizeName(base)
	if err != nil {
		return "app"
	}
	return normalized
}
