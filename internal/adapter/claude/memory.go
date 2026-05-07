package claude

import (
	"regexp"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

var importRe = regexp.MustCompile(`(?m)^@import\s+\./fragments/(\S+)\s*$`)

func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	if c.Memory.Body == "" {
		return nil, nil
	}
	body := importRe.ReplaceAllStringFunc(c.Memory.Body, func(line string) string {
		m := importRe.FindStringSubmatch(line)
		if len(m) < 2 {
			return line
		}
		if frag, ok := c.Memory.Fragments[m[1]]; ok {
			return strings.TrimRight(frag, "\n")
		}
		// Unknown fragment; preserve line so the user notices.
		return line
	})
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Memory,
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil
}
