// Package skills loads explicitly authorized policy skill packs and exposes
// their declared HTTP operations as Eino tools.
package skills

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Operation is one fixed HTTP operation declared by a policy pack.
type Operation struct {
	ID          string         `json:"id"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	GroupKey    string         `json:"groupKey"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Skill contains the frontmatter metadata and the on-demand instruction body.
type Skill struct {
	Name        string
	Description string
	Body        string
	Operations  []Operation
	file        string
}

// Catalog is the group-filtered set of skill instructions and HTTP contracts.
type Catalog struct {
	Skills     []Skill
	Operations []Operation
	root       string
}

// ClientConfig contains the trusted service endpoint and credentials.
type ClientConfig struct {
	BaseURL     string
	Token       string
	TokenHeader string
	ProjectID   string
	Timeout     time.Duration
}

var errInvalidCatalog = errors.New("invalid skill catalog")

func validPackName(name string) bool {
	if name == "" || strings.Contains(name, "..") || filepath.Base(name) != name {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return name[0] >= 'a' && name[0] <= 'z' || name[0] >= '0' && name[0] <= '9'
}

func genericError() error {
	return fmt.Errorf("%w: pack metadata or operation contract", errInvalidCatalog)
}
