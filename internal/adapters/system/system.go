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

// UUID v7 is time ordered: inserts land at the end of the primary key index
// instead of all over it.
func (UUIDGenerator) NewID() (uuid.UUID, error) {
	return uuid.NewV7()
}
