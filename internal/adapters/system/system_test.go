package system_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/vit3ro/transaction-execution-api/internal/adapters/system"
)

func TestClock_ReturnsCurrentTimeInUTC(t *testing.T) {
	before := time.Now()

	now := system.Clock{}.Now()

	if now.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", now.Location())
	}
	if now.Before(before.Add(-time.Second)) || now.After(before.Add(time.Second)) {
		t.Errorf("Now = %v, want a time close to %v", now, before)
	}
}

func TestUUIDGenerator_ProducesTimeOrderedIDs(t *testing.T) {
	generator := system.UUIDGenerator{}

	first, err := generator.NewID()
	if err != nil {
		t.Fatalf("NewID returned unexpected error: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := generator.NewID()
	if err != nil {
		t.Fatalf("NewID returned unexpected error: %v", err)
	}

	if first.Version() != 7 || second.Version() != 7 {
		t.Errorf("versions = (%d, %d), want UUID v7", first.Version(), second.Version())
	}
	if bytes.Compare(first[:], second[:]) >= 0 {
		t.Errorf("ids %s and %s are not in creation order", first, second)
	}
}
