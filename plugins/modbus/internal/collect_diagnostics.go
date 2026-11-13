package internal

import (
	"sync"
	"time"

	"go.uber.org/zap"
)

const collectLogInterval = 30 * time.Second

// Only currently failing/deferred groups occupy state. No goroutine, timer or
// global sampler is added; unrelated connectors and logger users are independent.
// The lock also covers explicit reads racing with scheduled collection.
type collectDiagnostics struct {
	mu          sync.Mutex
	failures    map[*pointGroup]*collectFailure
	deferred    map[*pointGroup]time.Time
	busySince   time.Time
	busyLastLog time.Time
	busyWarned  bool
	skipped     uint64
}

type collectFailure struct {
	since       time.Time
	lastFailure time.Time
	lastLog     time.Time
	err         string
	total       uint64
	pending     uint64
}

func collectGroupFields(key string, group *pointGroup) []zap.Field {
	return []zap.Field{
		zap.String("connectionKey", key),
		zap.Uint8("unitID", group.UnitID),
		zap.String("registerType", string(group.RegisterType)),
		zap.Uint16("address", group.Address),
		zap.Uint16("quantity", group.Quantity),
		zap.Int("pointCount", len(group.Points)),
		zap.Duration("baseInterval", group.Duration),
	}
}

func (d *collectDiagnostics) readFailed(log *zap.Logger, key string, group *pointGroup, err error, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failures == nil {
		d.failures = make(map[*pointGroup]*collectFailure)
	}
	f := d.failures[group]
	first := f == nil
	if first {
		f = &collectFailure{since: now}
		d.failures[group] = f
	}
	changed := f.err != err.Error()
	f.err = err.Error()
	f.lastFailure = now
	f.total++
	f.pending++
	if first || changed || now.Sub(f.lastLog) >= collectLogInterval {
		d.logFailure(log, key, group, f, now)
	}
	// Full model/point details are opt-in; never dump connection credentials.
	log.Debug("modbus collect read failure details", zap.String("connectionKey", key), zap.Any("group", group), zap.Error(err))
}

func (d *collectDiagnostics) logFailure(log *zap.Logger, key string, group *pointGroup, f *collectFailure, now time.Time) {
	fields := collectGroupFields(key, group)
	fields = append(fields, zap.String("error", f.err), zap.Uint64("totalFailures", f.total),
		zap.Uint64("newFailures", f.pending), zap.Duration("failureDuration", now.Sub(f.since)),
		zap.Duration("lastFailureAge", now.Sub(f.lastFailure)))
	log.Error("modbus collect read failed", fields...)
	f.lastLog = now
	f.pending = 0
}

func (d *collectDiagnostics) readSucceeded(log *zap.Logger, key string, group *pointGroup, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.failures[group]; f != nil {
		fields := collectGroupFields(key, group)
		fields = append(fields, zap.Uint64("totalFailures", f.total), zap.Uint64("unreportedFailures", f.pending),
			zap.Duration("failureDuration", now.Sub(f.since)))
		log.Info("modbus collect read recovered", fields...)
		delete(d.failures, group)
	}
}

// Count skipped group checks, not failed requests. Both write-yield branches
// call this method; an unchanged due group may be counted on several scans.
func (d *collectDiagnostics) deferRead(group *pointGroup, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deferred == nil {
		d.deferred = make(map[*pointGroup]time.Time)
	}
	if len(d.deferred) == 0 {
		d.busySince = now
	}
	if _, exists := d.deferred[group]; !exists {
		d.deferred[group] = now
	}
	d.skipped++
}

// A real read attempt ends write deferral, even if the subsequent I/O fails.
// This message deliberately does not claim that the device has recovered.
func (d *collectDiagnostics) readStarted(log *zap.Logger, key string, group *pointGroup, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.deferred[group]; !exists {
		return
	}
	delete(d.deferred, group)
	if len(d.deferred) != 0 {
		return
	}
	if d.busyWarned {
		log.Info("modbus write deferral cleared", zap.String("connectionKey", key),
			zap.Duration("deferralDuration", now.Sub(d.busySince)), zap.Uint64("skippedGroupChecks", d.skipped))
	}
	d.busySince = time.Time{}
	d.busyLastLog = time.Time{}
	d.busyWarned = false
	d.skipped = 0
}

func (d *collectDiagnostics) flush(log *zap.Logger, key string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Persistent faults remain visible during the existing (up to 1 minute)
	// retry backoff. newFailures=0 explicitly means no new failed I/O occurred.
	for group, f := range d.failures {
		if now.Sub(f.lastLog) >= collectLogInterval {
			d.logFailure(log, key, group, f, now)
		}
	}
	if len(d.deferred) == 0 || (d.busyWarned && now.Sub(d.busyLastLog) < collectLogInterval) {
		return
	}
	var oldest time.Time
	var sample *pointGroup
	for group, since := range d.deferred {
		if sample == nil || since.Before(oldest) {
			oldest, sample = since, group
		}
	}
	if now.Sub(oldest) < collectLogInterval {
		return
	}
	fields := collectGroupFields(key, sample)
	fields = append(fields, zap.Int("deferredGroups", len(d.deferred)), zap.Uint64("skippedGroupChecks", d.skipped),
		zap.Duration("longestDeferral", now.Sub(oldest)))
	log.Warn("modbus collection delayed by writes", fields...)
	d.busyLastLog = now
	d.busyWarned = true
}
