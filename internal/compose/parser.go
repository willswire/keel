package compose

import (
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

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image       string             `yaml:"image"`
	Build       composeBuild       `yaml:"build"`
	Ports       []composePort      `yaml:"ports"`
	Expose      []string           `yaml:"expose"`
	Environment yamlv3.Node        `yaml:"environment"`
	Command     yamlv3.Node        `yaml:"command"`
	Entrypoint  yamlv3.Node        `yaml:"entrypoint"`
	User        string             `yaml:"user"`
	Healthcheck composeHealthcheck `yaml:"healthcheck"`
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

// ParseFile parses a Docker Compose file into a normalized spec.
//
// This parser intentionally supports a focused subset of the compose spec:
// services.image, services.build, services.ports, services.expose,
// services.environment, services.command, services.entrypoint,
// services.user, and services.healthcheck.test.
func ParseFile(path string) (model.ComposeAppSpec, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return model.ComposeAppSpec{}, fmt.Errorf("read compose file %s: %w", path, err)
	}

	var file composeFile
	if err := yamlv3.Unmarshal(content, &file); err != nil {
		return model.ComposeAppSpec{}, fmt.Errorf("parse compose file %s: %w", path, err)
	}

	if len(file.Services) == 0 {
		return model.ComposeAppSpec{}, fmt.Errorf("compose file %s must define at least one service", path)
	}

	composeAbs, err := filepath.Abs(path)
	if err != nil {
		return model.ComposeAppSpec{}, fmt.Errorf("resolve compose file path: %w", err)
	}
	composeDir := filepath.Dir(composeAbs)

	projectName := strings.TrimSpace(file.Name)
	if projectName == "" {
		projectName = filepath.Base(composeDir)
	}
	projectName, err = normalizeName(projectName)
	if err != nil {
		return model.ComposeAppSpec{}, fmt.Errorf("invalid compose project name: %w", err)
	}

	keys := make([]string, 0, len(file.Services))
	for k := range file.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	services := make([]model.ComposeServiceSpec, 0, len(keys))
	seenNames := map[string]struct{}{}

	for _, key := range keys {
		rawSvc := file.Services[key]
		serviceName, err := normalizeName(key)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("invalid service name %q: %w", key, err)
		}
		if _, exists := seenNames[serviceName]; exists {
			return model.ComposeAppSpec{}, fmt.Errorf("duplicate normalized service name %q", serviceName)
		}
		seenNames[serviceName] = struct{}{}

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

		env, err := parseEnvironment(rawSvc.Environment)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q environment: %w", key, err)
		}
		command, err := parseCommandLike(rawSvc.Command)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q command: %w", key, err)
		}
		entrypoint, err := parseCommandLike(rawSvc.Entrypoint)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q entrypoint: %w", key, err)
		}
		healthcheck, err := parseHealthcheck(rawSvc.Healthcheck)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q healthcheck.test: %w", key, err)
		}
		ports, err := parsePorts(rawSvc.Ports, rawSvc.Expose)
		if err != nil {
			return model.ComposeAppSpec{}, fmt.Errorf("service %q ports: %w", key, err)
		}

		services = append(services, model.ComposeServiceSpec{
			Name:      serviceName,
			Namespace: projectName,
			Image:     image,
			Build:     buildSpec,
			Dockerfile: model.DockerfileSpec{
				Name:         serviceName,
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
	}, nil
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
	return &model.ComposeBuildSpec{
		ContextPath:    contextPath,
		DockerfilePath: dockerfilePath,
	}, nil
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
			// Be permissive and treat unknown mode as command payload.
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

var invalidNameRunes = regexp.MustCompile(`[^a-z0-9.-]+`)

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
