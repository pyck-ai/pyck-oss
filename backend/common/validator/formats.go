package validator

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

var once sync.Once

// registerCustomFormats registers custom format validators for EAN and UPC barcode formats.
// Supports EAN-13, EAN-8, UPC-A, and UPC-E with checksum and structural validation.
func registerCustomFormats() {
	once.Do(func() {
		jsonschema.Formats["ean13"] = func(v interface{}) bool {
			s, ok := v.(string)
			return ok && regexp.MustCompile(`^\d{13}$`).MatchString(s) && isValidEANChecksum(s)
		}

		jsonschema.Formats["ean8"] = func(v interface{}) bool {
			s, ok := v.(string)
			return ok && regexp.MustCompile(`^\d{8}$`).MatchString(s) && isValidEANChecksum(s)
		}

		jsonschema.Formats["upca"] = func(v interface{}) bool {
			s, ok := v.(string)
			return ok && regexp.MustCompile(`^\d{12}$`).MatchString(s) && isValidUPCChecksum(s)
		}

		jsonschema.Formats["upce"] = func(v interface{}) bool {
			s, ok := v.(string)
			if !ok || !regexp.MustCompile(`^\d{8}$`).MatchString(s) {
				return false
			}
			// Structural validation: only number system 0 or 1 are valid for UPC-E
			if s[0] != '0' && s[0] != '1' {
				return false
			}
			return isValidUPCEChecksum(s)
		}
	})
}

// isValidUPCEChecksum validates the checksum of a UPC-E barcode.
func isValidUPCEChecksum(upce string) bool {
	if len(upce) != 8 {
		return false
	}

	ns := upce[0:1]
	d := upce[1:7]
	check := upce[7:8]

	expanded, err := expandUPCEtoUPCA(ns, d)
	if err != nil {
		return false
	}

	// Compute check digit for expanded UPC-A (11 digits)
	sum := 0
	for i := 0; i < 11; i++ {
		digit := int(expanded[i] - '0')
		if i%2 == 0 {
			sum += digit * 3
		} else {
			sum += digit
		}
	}
	expected := (10 - (sum % 10)) % 10
	actual := int(check[0] - '0')

	return expected == actual
}

// expandUPCEtoUPCA expands a UPC-E barcode into its equivalent 11-digit UPC-A format (without check digit).
func expandUPCEtoUPCA(ns string, d string) (string, error) {
	var upca string
	switch d[5] {
	case '0', '1', '2':
		upca = ns + d[0:2] + d[5:6] + "00000" + d[2:5]
	case '3':
		upca = ns + d[0:3] + "00000" + d[3:5]
	case '4':
		upca = ns + d[0:4] + "00000" + d[4:5]
	default:
		upca = ns + d[0:5] + "0000" + d[5:6]
	}

	if len(upca) != 11 {
		return "", fmt.Errorf("expanded UPC-A must be 11 digits, got: %s", upca)
	}

	return upca, nil
}

// isValidEANChecksum checks the validity of EAN-8 and EAN-13 barcodes using their checksum algorithm.
func isValidEANChecksum(s string) bool {
	sum := 0
	for i := 0; i < len(s)-1; i++ {
		digit := int(s[i] - '0')
		if (len(s)-i)%2 == 0 {
			sum += digit * 3
		} else {
			sum += digit
		}
	}
	checkDigit := (10 - (sum % 10)) % 10
	return checkDigit == int(s[len(s)-1]-'0')
}

// isValidUPCChecksum checks the validity of UPC-A barcodes using their checksum algorithm.
func isValidUPCChecksum(s string) bool {
	if len(s) != 12 {
		return false
	}
	sum := 0
	for i := 0; i < 11; i++ {
		digit := int(s[i] - '0')
		if i%2 == 0 {
			sum += digit * 3
		} else {
			sum += digit
		}
	}
	checkDigit := (10 - (sum % 10)) % 10
	return checkDigit == int(s[11]-'0')
}
