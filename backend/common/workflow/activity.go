package workflow

type NextActivityProperties struct {
	NextActivityName       *string
	NextActivityProperties []*ActivityProperties
}

type ActivityProperties struct {
	FormKey string
	Value   string
}

type ActivityDetails struct {
	Name       string            `json:"name"`
	Properties []*KeyValueStruct `json:"properties,omitempty"`
}
