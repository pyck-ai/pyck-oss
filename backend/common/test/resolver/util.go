package resolver

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func reindent(str, prefix string) string {
	lines := strings.Split(str, "\n")
	minIndent := -1
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		minIndent = 0
	}
	for i, line := range lines {
		if len(line) > minIndent {
			lines[i] = line[minIndent:]
		} else {
			lines[i] = ""
		}
	}

	return strings.TrimSpace(strings.Join(lines, "\n"+prefix))
}

func EscapeJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	s = strings.ReplaceAll(s, "\b", `\b`)
	s = strings.ReplaceAll(s, "\f", `\f`)
	return s
}

func DatabaseURI(t *testing.T) string {
	t.Helper()

	return fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_fk=1", t.Name(), time.Now().UnixNano())
}
