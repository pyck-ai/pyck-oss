package workflow

import (
	"fmt"
	"strings"
)

type KeyValueStruct struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func mapToQuery(filters map[string]string) string {
	conditions := make([]string, 0, len(filters))
	for key, value := range filters {
		condition := fmt.Sprintf(`%s = "%s"`, key, value)
		conditions = append(conditions, condition)
	}

	query := strings.Join(conditions, " AND ")

	return query
}
