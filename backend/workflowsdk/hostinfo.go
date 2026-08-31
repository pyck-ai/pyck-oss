package workflowsdk

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	temporalworker "go.temporal.io/sdk/worker"
)

const (
	// defaultWorkerSlots is what the SDK's own fixed-size tuner uses for every
	// slot type, so the tuner below hands out slots exactly like the one it
	// replaces.
	defaultWorkerSlots = 1000

	// defaultCgroupRoot is the cgroup v2 mount every containerised worker sees.
	defaultCgroupRoot = "/sys/fs/cgroup"
)

// sysInfoSlotSupplier delegates every slot decision and additionally answers
// HasSysInfoProvider, which is the only way host CPU and memory reach the
// heartbeat. Of the suppliers shipped with the SDK just the resource-based one
// implements it, and adopting that would also make slot allocation adaptive.
//
// The SDK labels a supplier by concrete type, so wrapping makes Temporal's UI
// call these slots "Custom" rather than "Fixed". The counts it shows stay right.
type sysInfoSlotSupplier struct {
	temporalworker.SlotSupplier
	provider temporalworker.SysInfoProvider
}

//nolint:ireturn // the signature is dictated by temporalworker.HasSysInfoProvider.
func (s sysInfoSlotSupplier) SysInfoProvider() temporalworker.SysInfoProvider {
	return s.provider
}

// cgroupInfo reports the pod's own resource use, matching the cgroup branch of
// go.temporal.io/sdk/contrib/sysinfo. Where that module falls back to host-wide
// gopsutil readings this deliberately does not: a worker's share of the node
// says little about the worker. The module is unusable here anyway — it requires
// go.temporal.io/api v1.62.11, which a workspace resolves for every module,
// breaking backend/temporal against go.temporal.io/server v1.31.x.
//
// Outside a Linux container the files are absent and readings are 0, which is
// what workers reported before any of this.
type cgroupInfo struct {
	root string

	mu            sync.Mutex
	lastSampledAt time.Time
	lastCPUMicros uint64
}

func newCgroupInfo() *cgroupInfo {
	return &cgroupInfo{root: defaultCgroupRoot}
}

func (c *cgroupInfo) MemoryUsage(*temporalworker.SysInfoContext) (float64, error) {
	used, err := c.readUint("memory.current")
	if err != nil || used == 0 {
		return 0, err
	}

	limit, err := c.readUint("memory.max")
	if err != nil || limit == 0 {
		return 0, err
	}

	return clampFraction(float64(used) / float64(limit)), nil
}

// CpuUsage is a delta between calls, so the first one has nothing to compare
// against and reports 0.
func (c *cgroupInfo) CpuUsage(*temporalworker.SysInfoContext) (float64, error) {
	micros, err := c.readCPUMicros()
	if err != nil || micros == 0 {
		return 0, err
	}

	cores, err := c.readCPUCores()
	if err != nil || cores == 0 {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	previousMicros, previousAt := c.lastCPUMicros, c.lastSampledAt
	c.lastCPUMicros, c.lastSampledAt = micros, time.Now()

	// A counter that went backwards means the cgroup was replaced under us.
	if previousAt.IsZero() || micros < previousMicros {
		return 0, nil
	}

	elapsed := c.lastSampledAt.Sub(previousAt).Microseconds()
	if elapsed <= 0 {
		return 0, nil
	}

	return clampFraction(float64(micros-previousMicros) / (float64(elapsed) * cores)), nil
}

// read returns an empty string for an absent file: not being in a cgroup is a
// reason to report nothing, not an error.
func (c *cgroupInfo) read(name string) string {
	raw, err := os.ReadFile(filepath.Join(c.root, name))
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(raw))
}

// readUint returns 0 for an absent file or the literal "max", both of which
// mean "no limit to measure against".
func (c *cgroupInfo) readUint(name string) (uint64, error) {
	text := c.read(name)
	if text == "" || text == "max" {
		return 0, nil
	}

	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cgroup %s: %w", name, err)
	}

	return value, nil
}

// readCPUMicros returns the usage_usec counter out of cpu.stat.
func (c *cgroupInfo) readCPUMicros() (uint64, error) {
	for line := range strings.Lines(c.read("cpu.stat")) {
		field, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found || field != "usage_usec" {
			continue
		}

		micros, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cgroup cpu.stat usage_usec: %w", err)
		}

		return micros, nil
	}

	return 0, nil
}

// readCPUCores turns cpu.max ("<quota> <period>") into the cores the cgroup may
// use, falling back to what the runtime can schedule on when no quota is set.
//
// That fallback carries the feature: pyck sets no CPU limits, to avoid CFS
// throttling, so cpu.max reads "max <period>" on every worker and treating it as
// zero cores would report 0 forever. GOMAXPROCS is itself cgroup-aware, so it
// follows a quota if one is ever introduced.
func (c *cgroupInfo) readCPUCores() (float64, error) {
	quotaText, periodText, found := strings.Cut(c.read("cpu.max"), " ")
	if !found || quotaText == "max" {
		return float64(runtime.GOMAXPROCS(0)), nil
	}

	quota, err := strconv.ParseFloat(quotaText, 64)
	if err != nil {
		return 0, fmt.Errorf("cgroup cpu.max quota: %w", err)
	}

	period, err := strconv.ParseFloat(periodText, 64)
	if err != nil {
		return 0, fmt.Errorf("cgroup cpu.max period: %w", err)
	}

	if period == 0 {
		return float64(runtime.GOMAXPROCS(0)), nil
	}

	return quota / period, nil
}

func clampFraction(value float64) float64 {
	return min(max(value, 0), 1)
}

type slotCounts struct {
	workflow, activity, localActivity, nexus int
}

// takeSlotCounts moves the caller's MaxConcurrent* limits out of options and
// clears them: the SDK panics when they coexist with a Tuner, and translates
// them the same way itself when there is none. Unset fields fall back to its
// default.
func takeSlotCounts(options *temporalworker.Options) slotCounts {
	counts := slotCounts{
		workflow:      options.MaxConcurrentWorkflowTaskExecutionSize,
		activity:      options.MaxConcurrentActivityExecutionSize,
		localActivity: options.MaxConcurrentLocalActivityExecutionSize,
		nexus:         options.MaxConcurrentNexusTaskExecutionSize,
	}

	for _, count := range []*int{
		&counts.workflow, &counts.activity, &counts.localActivity, &counts.nexus,
	} {
		if *count <= 0 {
			*count = defaultWorkerSlots
		}
	}

	options.MaxConcurrentWorkflowTaskExecutionSize = 0
	options.MaxConcurrentActivityExecutionSize = 0
	options.MaxConcurrentLocalActivityExecutionSize = 0
	options.MaxConcurrentNexusTaskExecutionSize = 0

	return counts
}

// setHostInfoTuner gives options a fixed-size tuner with host resource reporting
// attached, leaving a tuner the caller already chose alone. Without it Temporal's
// UI shows "no usage data is available for this Worker" on every worker page.
func setHostInfoTuner(options *temporalworker.Options) error {
	if options.Tuner != nil {
		return nil
	}

	counts := takeSlotCounts(options)

	var tuning temporalworker.CompositeTunerOptions
	for _, slot := range []struct {
		supplier *temporalworker.SlotSupplier
		count    int
	}{
		{&tuning.WorkflowSlotSupplier, counts.workflow},
		{&tuning.ActivitySlotSupplier, counts.activity},
		{&tuning.LocalActivitySlotSupplier, counts.localActivity},
		{&tuning.NexusSlotSupplier, counts.nexus},
		// Sessions borrow the activity limit, as they do inside the SDK.
		{&tuning.SessionActivitySlotSupplier, counts.activity},
	} {
		supplier, err := temporalworker.NewFixedSizeSlotSupplier(slot.count)
		if err != nil {
			return fmt.Errorf("slot supplier: %w", err)
		}
		*slot.supplier = supplier
	}

	tuning.WorkflowSlotSupplier = sysInfoSlotSupplier{
		SlotSupplier: tuning.WorkflowSlotSupplier,
		provider:     newCgroupInfo(),
	}

	tuner, err := temporalworker.NewCompositeTuner(tuning)
	if err != nil {
		return fmt.Errorf("composite tuner: %w", err)
	}
	options.Tuner = tuner

	return nil
}
