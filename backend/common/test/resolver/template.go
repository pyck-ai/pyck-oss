package resolver

import (
	"bytes"
	"fmt"
	"text/template"
)

func ParseTemplate(tmpl string) Template {
	t := template.Must(template.New("template").Parse(tmpl))
	return Template{t}
}

type Template struct {
	*template.Template
}

func (t Template) RenderTemplate(data any) string {
	var result bytes.Buffer

	if err := t.Execute(&result, data); err != nil {
		panic(fmt.Sprintf("failed to execute template: %v", err))
	}

	return reindent(result.String(), "")
}
