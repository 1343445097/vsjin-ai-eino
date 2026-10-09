package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type operationFile struct {
	GroupKeys  []string    `json:"groupKeys"`
	Operations []Operation `json:"operations"`
}

// Load reads policy packs from root. Nil groups select knowledge.base,
// knowledge.cad, and scai.reporting; an explicit empty list selects no groups.
// The wildcard "*" selects all groups.
func Load(root string, groups []string) (Catalog, error) {
	var catalog Catalog
	absolute, err := filepath.Abs(root)
	if err != nil {
		return catalog, fmt.Errorf("load skill packs: invalid root")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return catalog, fmt.Errorf("load skill packs: invalid root")
	}
	catalog.root = absolute
	if groups == nil {
		groups = []string{"knowledge.base", "knowledge.cad", "scai.reporting"}
	}
	allowed := make(map[string]bool, len(groups))
	all := false
	for _, group := range groups {
		if group == "*" {
			all = true
		} else if group != "" {
			allowed[group] = true
		}
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return Catalog{}, fmt.Errorf("load skill packs: unable to read catalog")
	}
	seenByID := map[string]Operation{}
	seenByRoute := map[string]Operation{}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			return Catalog{}, fmt.Errorf("load skill packs: symlinked pack rejected")
		}
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !validPackName(name) {
			return Catalog{}, genericError()
		}
		dir := filepath.Join(absolute, name)
		meta, body, err := readSkill(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Catalog{}, genericError()
		}
		if meta["name"] != name || meta["description"] == "" {
			return Catalog{}, genericError()
		}
		var ops []Operation
		contractPath := filepath.Join(dir, "http-tools.json")
		packGroups := []string{}
		if _, statErr := os.Lstat(contractPath); statErr == nil {
			data, readErr := readRegularFile(contractPath)
			if readErr != nil {
				return Catalog{}, genericError()
			}
			var file operationFile
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&file) != nil {
				return Catalog{}, genericError()
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return Catalog{}, genericError()
			}
			packGroups = file.GroupKeys
			for _, op := range file.Operations {
				if !validOperation(op) {
					return Catalog{}, genericError()
				}
				route := op.Method + " " + op.Path
				previousID, hasID := seenByID[op.ID]
				previousRoute, hasRoute := seenByRoute[route]
				if hasID && !reflect.DeepEqual(previousID, op) || hasRoute && !reflect.DeepEqual(previousRoute, op) {
					return Catalog{}, genericError()
				}
				isNew := !hasID && !hasRoute
				if isNew {
					seenByID[op.ID] = op
					seenByRoute[route] = op
				}
				if all || allowed[op.GroupKey] {
					ops = append(ops, op)
					if isNew {
						catalog.Operations = append(catalog.Operations, op)
					}
				}
			}
		} else if !os.IsNotExist(statErr) {
			return Catalog{}, genericError()
		}
		if len(packGroups) == 0 {
			packGroups = []string{meta["groupKey"]}
		}
		packAllowed := all
		for _, group := range packGroups {
			packAllowed = packAllowed || allowed[group]
		}
		if !packAllowed && len(ops) == 0 {
			continue
		}
		skill := Skill{Name: meta["name"], Description: meta["description"], Body: body, Operations: ops, file: filepath.Join(dir, "SKILL.md")}
		catalog.Skills = append(catalog.Skills, skill)
	}
	sort.Slice(catalog.Skills, func(i, j int) bool { return catalog.Skills[i].Name < catalog.Skills[j].Name })
	sort.Slice(catalog.Operations, func(i, j int) bool { return catalog.Operations[i].ID < catalog.Operations[j].ID })
	return catalog, nil
}

func validOperation(op Operation) bool {
	if op.ID == "" || op.GroupKey == "" || op.Description == "" || op.InputSchema == nil {
		return false
	}
	if op.Method != "GET" && op.Method != "POST" {
		return false
	}
	if !strings.HasPrefix(op.Path, "/") || strings.HasPrefix(op.Path, "//") || strings.ContainsAny(op.Path, "?#\\") || strings.Contains(op.Path, "..") {
		return false
	}
	return true
}

func readSkill(path string) (map[string]string, string, error) {
	data, err := readRegularFile(path)
	if err != nil {
		return nil, "", err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, "", fmt.Errorf("missing frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return nil, "", fmt.Errorf("invalid frontmatter")
	}
	meta := map[string]string{}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, "", fmt.Errorf("invalid frontmatter")
		}
		meta[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"'")
	}
	return meta, strings.TrimSpace(text[4+end+5:]), nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("not a regular file")
	}
	return os.ReadFile(path)
}

// Prompt returns the compact first-turn catalog, without loading instruction bodies.
func (c Catalog) Prompt() string {
	var b strings.Builder
	b.WriteString("Use skills_read({skill}) to read skill instructions and skill_http_request({method, path, body}) only for declared routes.\n")
	for _, skill := range c.Skills {
		fmt.Fprintf(&b, "- %s: %s", skill.Name, skill.Description)
		if len(skill.Operations) > 0 {
			b.WriteString(" Routes: ")
			for i, op := range skill.Operations {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s %s", op.Method, op.Path)
			}
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// Read returns the instruction body for an authorized skill name.
func (c Catalog) Read(name string) (string, error) {
	if !validPackName(name) {
		return "", fmt.Errorf("skill not available")
	}
	for _, skill := range c.Skills {
		if skill.Name != name {
			continue
		}
		if c.root == "" {
			return skill.Body, nil
		}
		packDir := filepath.Dir(skill.file)
		info, err := os.Lstat(packDir)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("skill not available")
		}
		path := filepath.Join(c.root, filepath.Base(packDir), "SKILL.md")
		data, err := readRegularFile(path)
		if err != nil {
			return "", fmt.Errorf("skill not available")
		}
		_, body, err := readSkillData(data)
		if err != nil {
			return "", fmt.Errorf("skill not available")
		}
		return body, nil
	}
	return "", fmt.Errorf("skill not available")
}

func readSkillData(data []byte) (map[string]string, string, error) {
	// Reuse the same parser while keeping Read independent of a filesystem path.
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, "", fmt.Errorf("invalid frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return nil, "", fmt.Errorf("invalid frontmatter")
	}
	return map[string]string{}, strings.TrimSpace(text[4+end+5:]), nil
}
