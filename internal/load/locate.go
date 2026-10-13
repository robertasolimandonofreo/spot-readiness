package load

import (
	"os"
	"path/filepath"
	"strings"

	goyaml "sigs.k8s.io/yaml/goyaml.v3"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// source is where an object came from: its file and its parsed YAML, so findings can point to a line.
type source struct {
	file   string
	node   *goyaml.Node // the object's mapping node
	offset int          // lines before the document in its file
}

// displayPath is the file as shown in reports and SARIF: relative to the current directory when it
// is inside it, with forward slashes.
func displayPath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		if wd, err := os.Getwd(); err == nil {
			if rel, err := filepath.Rel(wd, abs); err == nil && !strings.HasPrefix(rel, "..") {
				p = rel
			}
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// splitPath splits on dots that are not inside [brackets], so a container named "my.app" works.
func splitPath(p string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range p {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
		case '.':
			if depth == 0 {
				out = append(out, p[start:i])
				start = i + 1
			}
		}
	}
	return append(out, p[start:])
}

// locator implements rules.Locator over a YAML node.
type locator struct {
	src  source
	kind string
}

func key(n *goyaml.Node, name string) (k, v *goyaml.Node) {
	if n == nil || n.Kind != goyaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == name {
			return n.Content[i], n.Content[i+1]
		}
	}
	return nil, nil
}

// step moves down one path segment: "name", or "name[item]" to pick the sequence element whose
// "name" field is item. It returns the node reached and the line to report for it.
func step(n *goyaml.Node, seg string) (*goyaml.Node, int) {
	name, sel := seg, ""
	if i := strings.Index(seg, "["); i > 0 && strings.HasSuffix(seg, "]") {
		name, sel = seg[:i], seg[i+1:len(seg)-1]
	}
	k, v := key(n, name)
	if k == nil {
		return nil, 0
	}
	if sel == "" || v.Kind != goyaml.SequenceNode {
		return v, k.Line
	}
	for _, item := range v.Content {
		if _, nv := key(item, "name"); nv != nil && nv.Value == sel {
			return item, item.Line
		}
	}
	return v, k.Line
}

// Line returns the 1-based line of the field in its file, or of the object's name when the field
// is not in the manifest (for example a setting left at its default).
func (l *locator) Line(path string) int {
	n := l.src.node
	line := n.Line
	if _, md := key(n, "metadata"); md != nil {
		if _, name := key(md, "name"); name != nil {
			line = name.Line
		}
	}
	var segs []string
	switch {
	case path == "pod" || strings.HasPrefix(path, "pod."):
		segs = splitPath(rules.PodSpecPath(l.kind))
		if rest := strings.TrimPrefix(strings.TrimPrefix(path, "pod"), "."); rest != "" {
			segs = append(segs, splitPath(rest)...)
		}
	case path == "job" || strings.HasPrefix(path, "job."):
		segs = splitPath(rules.JobSpecPath(l.kind))
		if rest := strings.TrimPrefix(strings.TrimPrefix(path, "job"), "."); rest != "" {
			segs = append(segs, splitPath(rest)...)
		}
	case path != "":
		segs = splitPath(path)
	}
	for _, seg := range segs {
		next, nextLine := step(n, seg)
		if next == nil {
			break
		}
		n, line = next, nextLine
	}
	return line + l.src.offset
}
