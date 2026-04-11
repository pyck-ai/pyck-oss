package temporalsetup

type TemporalSetupWorkflowInput struct {
	Namespace        string
	SearchAttributes map[string]string
}

var SearchAttributeTypes map[string]int32 = map[string]int32{
	"Unspecified": 0,
	"Text":        1,
	"Keyword":     2,
	"Int":         3,
	"Double":      4,
	"Bool":        5,
	"Datetime":    6,
}

type addSearchAttributesInput struct {
	TemporalUrl      string
	Namespace        string
	SearchAttributes map[string]string
}
