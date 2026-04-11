package validator

import (
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func init() {
	registerCustomFormats()
}

func TestEAN13Format(t *testing.T) {
	valid := []string{
		"5901234123457", // valid EAN-13
		"4006381333931", // valid EAN-13
	}
	invalid := []string{
		"5901234123456",  // invalid checksum
		"123456789012",   // too short
		"12345678901234", // too long
		"ABCDEFGHIJKLM",  // non-digit
	}

	for _, code := range valid {
		if !jsonschema.Formats["ean13"](code) {
			t.Errorf("ean13: expected valid but got invalid for %s", code)
		}
	}

	for _, code := range invalid {
		if jsonschema.Formats["ean13"](code) {
			t.Errorf("ean13: expected invalid but got valid for %s", code)
		}
	}
}

func TestEAN8Format(t *testing.T) {
	valid := []string{
		"12345670", // valid EAN-8
	}
	invalid := []string{
		"12345671",  // invalid checksum
		"1234567",   // too short
		"123456789", // too long
		"ABCDEFGH",  // non-digit
	}

	for _, code := range valid {
		if !jsonschema.Formats["ean8"](code) {
			t.Errorf("ean8: expected valid but got invalid for %s", code)
		}
	}

	for _, code := range invalid {
		if jsonschema.Formats["ean8"](code) {
			t.Errorf("ean8: expected invalid but got valid for %s", code)
		}
	}
}

func TestUPCAFormat(t *testing.T) {
	valid := []string{
		"036000291452", // valid UPC-A
		"042100005264", // valid UPC-A
	}
	invalid := []string{
		"036000291453",  // invalid checksum
		"12345678901",   // too short
		"1234567890123", // too long
		"ABCDEFGHIJKL",  // non-digit
	}

	for _, code := range valid {
		if !jsonschema.Formats["upca"](code) {
			t.Errorf("upca: expected valid but got invalid for %s", code)
		}
	}

	for _, code := range invalid {
		if jsonschema.Formats["upca"](code) {
			t.Errorf("upca: expected invalid but got valid for %s", code)
		}
	}
}

func TestUPCEFormat(t *testing.T) {
	valid := []string{
		"01234565", // valid UPC-E
	}

	invalid := []string{
		"0123456",   // too short
		"012345678", // too long
		"ABCDEFGH",  // non-digit
		"21234565",  // invalid number system (only 0 and 1 supported)
		"04210005",  // invalid UPC-E structure (will fail expansion or checksum)
		"01234566",  // incorrect checksum
	}

	for _, code := range valid {
		if !jsonschema.Formats["upce"](code) {
			t.Errorf("upce: expected valid but got invalid for %s", code)
		}
	}

	for _, code := range invalid {
		if jsonschema.Formats["upce"](code) {
			t.Errorf("upce: expected invalid but got valid for %s", code)
		}
	}
}
