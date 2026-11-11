package system

import (
	"time"

	"github.com/google/uuid"

	"github.com/vit3ro/transaction-execution-api/internal/ports"
)

var (
	_ ports.Clock       = Clock{}
	_ ports.IDGenerator = UUIDGenerator{}
)

type Clock struct{}

func (Clock) Now() time.Time {
	return time.Now().UTC()
}

type UUIDGenerator struct{}

func (UUIDGenerator) NewID() (uuid.UUID, error) {
	return uuid.NewV7()
}
