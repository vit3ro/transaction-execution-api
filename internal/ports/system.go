package ports

import (
	"time"

	"github.com/google/uuid"
)

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID() (uuid.UUID, error)
}
