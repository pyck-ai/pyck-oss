package uuidgql

import (
	"github.com/google/uuid"
)

func GenerateV7UUID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

func NilUUID() uuid.UUID {
	return uuid.Nil
}
