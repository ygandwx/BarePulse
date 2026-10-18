package drivers

import (
	"context"
	"errors"
	"time"
)

var (
	ErrConsoleTargetUnknown = errors.New("console target cannot be verified")
	ErrConsoleTargetChanged = errors.New("console target changed")
	ErrConsoleGuardClosed   = errors.New("console target guard closed")
)

// ConsoleTargetGuard cancels its context on the first unknown or different
// target. Cancellation is latched; a later matching observation cannot reopen
// access. This is one prerequisite for a future real gateway, not proof of
// video/HID/media isolation. The caller must hold the durable whole-device lease
// and close every transport when Context is cancelled. No device write is made.
type ConsoleTargetGuard struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func (g *ConsoleTargetGuard) Context() context.Context { return g.ctx }

// Close cancels outstanding HTTP reads and waits for the poller to stop. It does
// not clear a persisted lease, release protection, switch ports or reset a KVM.
func (g *ConsoleTargetGuard) Close() {
	g.cancel(ErrConsoleGuardClosed)
	<-g.done
}

// WatchConsoleTarget uses only the custom firmware's known selected-port GET.
// A successful initial read precedes creation of the guard. Polling can detect
// drift, but cannot eliminate the interval between an out-of-band switch and
// its detection; exclusive physical management and channel verification remain
// separate deployment gates. The production gateway is not enabled by this API.
func (d *PiKVM) WatchConsoleTarget(parent context.Context, t Target, interval time.Duration) (*ConsoleTargetGuard, error) {
	if t.Port < 1 || t.Port > 16 || !d.mappingVerified {
		return nil, ErrConsoleTargetUnknown
	}
	if interval == 0 {
		interval = time.Second
	}
	if interval < 250*time.Millisecond || interval > 5*time.Second {
		return nil, errors.New("console target polling interval must be between 250ms and 5s")
	}
	ctx, cancel := context.WithCancelCause(parent)
	check := func() error {
		r := d.SelectedPort(ctx, t)
		if r.Status != "succeeded" || !r.Completed || r.SelectedPort == nil {
			return ErrConsoleTargetUnknown
		}
		if *r.SelectedPort != t.Port {
			return ErrConsoleTargetChanged
		}
		return nil
	}
	if err := check(); err != nil {
		cancel(err)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		cancel(err)
		return nil, err
	}
	g := &ConsoleTargetGuard{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(g.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := check(); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return g, nil
}
