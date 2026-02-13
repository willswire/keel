package dockerfile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/willswire/keel/internal/model"
)

// ParseFile parses a Dockerfile into a normalized spec for the final build stage.
func ParseFile(path string) (model.DockerfileSpec, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return model.DockerfileSpec{}, fmt.Errorf("read Dockerfile %s: %w", path, err)
	}

	lines := flattenLines(string(content))
	if len(lines) == 0 {
		return model.DockerfileSpec{}, fmt.Errorf("Dockerfile %s is empty", filepath.Clean(path))
	}

	stage := model.DockerfileSpec{}
	haveFrom := false

	for _, line := range lines {
		instruction, rest, ok := splitInstruction(line)
		if !ok {
			continue
		}

		switch instruction {
		case "FROM":
			haveFrom = true
			stage = model.DockerfileSpec{}
		case "EXPOSE":
			if !haveFrom {
				continue
			}
			for _, p := range parseExpose(rest) {
				stage.ExposedPorts = append(stage.ExposedPorts, p)
			}
		case "ENV":
			if !haveFrom {
				continue
			}
			stage.Env = append(stage.Env, parseEnv(rest)...)
		case "LABEL":
			if !haveFrom {
				continue
			}
			if name := parseNameLabel(rest); name != "" {
				stage.Name = name
			}
		case "USER":
			if !haveFrom {
				continue
			}
			stage.User = strings.TrimSpace(rest)
		case "CMD":
			if !haveFrom {
				continue
			}
			stage.Cmd = strings.TrimSpace(rest)
		case "ENTRYPOINT":
			if !haveFrom {
				continue
			}
			stage.Entrypoint = strings.TrimSpace(rest)
		case "HEALTHCHECK":
			if !haveFrom {
				continue
			}
			stage.Healthcheck = strings.TrimSpace(rest)
		}
	}

	if !haveFrom {
		return model.DockerfileSpec{}, fmt.Errorf("Dockerfile must contain FROM")
	}

	stage.ExposedPorts = dedupeAndSortPorts(stage.ExposedPorts)
	stage.Env = dedupeEnv(stage.Env)

	if len(stage.ExposedPorts) == 0 {
		return model.DockerfileSpec{}, fmt.Errorf("Dockerfile is missing required EXPOSE instruction in final stage")
	}
	if strings.TrimSpace(stage.Name) == "" {
		return model.DockerfileSpec{}, fmt.Errorf("Dockerfile is missing required LABEL NAME instruction in final stage")
	}
	if strings.TrimSpace(stage.User) == "" {
		return model.DockerfileSpec{}, fmt.Errorf("Dockerfile is missing required USER instruction in final stage")
	}

	return stage, nil
}

func flattenLines(content string) []string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	lines := []string{}
	var b strings.Builder

	flush := func() {
		line := strings.TrimSpace(b.String())
		if line != "" {
			lines = append(lines, line)
		}
		b.Reset()
	}

	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "#") && b.Len() == 0 {
			continue
		}
		continued := strings.HasSuffix(raw, "\\")
		raw = strings.TrimSuffix(raw, "\\")
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(raw)
		if !continued {
			flush()
		}
	}
	flush()
	return lines
}

func splitInstruction(line string) (string, string, bool) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return "", "", false
	}
	ins := strings.ToUpper(parts[0])
	rest := strings.TrimSpace(line[len(parts[0]):])
	return ins, rest, true
}

func parseExpose(raw string) []model.Port {
	out := []model.Port{}
	for _, token := range strings.Fields(raw) {
		if token == "" {
			continue
		}

		portProto := strings.SplitN(token, "/", 2)
		portPart := portProto[0]
		if strings.Contains(portPart, "-") {
			portPart = strings.SplitN(portPart, "-", 2)[0]
		}
		num, err := strconv.Atoi(portPart)
		if err != nil {
			continue
		}
		proto := "TCP"
		if len(portProto) > 1 && portProto[1] != "" {
			proto = strings.ToUpper(portProto[1])
		}
		out = append(out, model.Port{Number: num, Protocol: proto, Raw: token})
	}
	return out
}

func parseEnv(raw string) []model.EnvVar {
	items := []model.EnvVar{}
	fields := splitShellWords(raw)
	if len(fields) == 0 {
		return items
	}

	if strings.Contains(fields[0], "=") {
		for _, field := range fields {
			kv := strings.SplitN(field, "=", 2)
			if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" {
				continue
			}
			items = append(items, model.EnvVar{
				Name:  strings.TrimSpace(kv[0]),
				Value: trimWrappingQuotes(strings.TrimSpace(kv[1])),
			})
		}
		return items
	}

	for i := 0; i+1 < len(fields); i += 2 {
		name := strings.TrimSpace(fields[i])
		value := trimWrappingQuotes(strings.TrimSpace(fields[i+1]))
		if name == "" {
			continue
		}
		items = append(items, model.EnvVar{Name: name, Value: value})
	}
	return items
}

func splitShellWords(raw string) []string {
	var out []string
	var b strings.Builder
	inQuote := rune(0)
	escaped := false

	flush := func() {
		if b.Len() == 0 {
			return
		}
		out = append(out, b.String())
		b.Reset()
	}

	for _, r := range raw {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			inQuote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return out
}

func trimWrappingQuotes(in string) string {
	if len(in) < 2 {
		return in
	}
	if (strings.HasPrefix(in, `"`) && strings.HasSuffix(in, `"`)) || (strings.HasPrefix(in, `'`) && strings.HasSuffix(in, `'`)) {
		return in[1 : len(in)-1]
	}
	return in
}

func parseNameLabel(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}

	if strings.Contains(fields[0], "=") {
		for _, field := range fields {
			kv := strings.SplitN(field, "=", 2)
			if len(kv) != 2 {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(kv[0]), "NAME") {
				continue
			}
			return strings.Trim(strings.TrimSpace(kv[1]), `"'`)
		}
		return ""
	}

	for i := 0; i+1 < len(fields); i += 2 {
		if !strings.EqualFold(strings.TrimSpace(fields[i]), "NAME") {
			continue
		}
		return strings.Trim(strings.TrimSpace(fields[i+1]), `"'`)
	}
	return ""
}

func dedupeAndSortPorts(in []model.Port) []model.Port {
	seen := map[string]struct{}{}
	out := make([]model.Port, 0, len(in))
	for _, p := range in {
		key := fmt.Sprintf("%d/%s", p.Number, p.Protocol)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Number == out[j].Number {
			return out[i].Protocol < out[j].Protocol
		}
		return out[i].Number < out[j].Number
	})
	return out
}

func dedupeEnv(in []model.EnvVar) []model.EnvVar {
	if len(in) == 0 {
		return nil
	}
	last := map[string]string{}
	for _, e := range in {
		if strings.TrimSpace(e.Name) == "" {
			continue
		}
		last[e.Name] = e.Value
	}
	keys := make([]string, 0, len(last))
	for k := range last {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]model.EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, model.EnvVar{Name: k, Value: last[k]})
	}
	return out
}
