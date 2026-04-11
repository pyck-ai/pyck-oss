package tests

import (
	"testing"

	"github.com/joho/godotenv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNatsSuite(t *testing.T) {
	// Load .env file
	err := godotenv.Load("../../../../.env")
	if err != nil {
		t.Fatalf("Error loading .env file: %v", err)
	}

	RegisterFailHandler(Fail)
	RunSpecs(t, "NATS Integration Suite")
}
