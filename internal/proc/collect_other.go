//go:build !darwin && !linux

package proc

import (
	"context"
	"errors"
	"time"
)

// System is not implemented outside macOS yet.
type System struct{}

// Collect reports that this platform is not supported yet.
func (System) Collect(context.Context) (*Snapshot, error) {
	return nil, errors.New("tidewake supports macOS and Linux only")
}

// SampleCPU reports that this platform is not supported yet.
func (System) SampleCPU(context.Context) (map[int]time.Duration, error) {
	return nil, errors.New("tidewake supports macOS and Linux only")
}
