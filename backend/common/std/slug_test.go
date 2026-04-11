package std

import "testing"

type caseType struct {
	input    string
	expected string
}

var slugTestCases = []caseType{
	{"", ""},
	{"Hello World", "hello-world"},
	{"  Hello  World! ", "hello-world"},
	{"--Hello --World--", "hello-world"},
	{"pyck is Awesome", "pyck-is-awesome"},
	{"  Multiple   Spaces  ", "multiple-spaces"},
	{"Special@#%Characters", "special-characters"},
	{"Trailing-and-leading--", "trailing-and-leading"},
	{"Numbers 123", "numbers-123"},
	{"123 Numbers", "123-numbers"},
	{"Mix3d C4se", "mix3d-c4se"},
	{" 123 456 ", "123-456"},
	{"Non-Alpha-Numeric!@#$%^&*()_+", "non-alpha-numeric"},
	{"Symbols *&^%$#@!~`", "symbols"},
	{"Punctuation.,:;?!", "punctuation"},
	{"Mixed -- Characters!!", "mixed-characters"},
	{"Combination of 123 and !@#", "combination-of-123-and"},
}

func TestToSlug(t *testing.T) {
	for _, testCase := range slugTestCases {
		actual := ToSlug(testCase.input)
		if actual != testCase.expected {
			t.Errorf("ToSlug(%s) = %s; want %s", testCase.input, actual, testCase.expected)
		}
	}
}
