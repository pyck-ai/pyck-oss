//nolint:testpackage // white-box tests: they drive the unexported cgroup reader and tuner.
package workflowsdk

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	temporalworker "go.temporal.io/sdk/worker"
)

// cgroupFixture returns a reader backed by a temp dir holding the given files.
func cgroupFixture(t *testing.T, files map[string]string) *cgroupInfo {
	t.Helper()

	root := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(content), 0o600))
	}

	return &cgroupInfo{root: root}
}

func TestCgroupMemoryUsage(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		files map[string]string
		want  float64
	}{
		"fraction of the limit": {
			files: map[string]string{"memory.current": "512\n", "memory.max": "1024\n"},
			want:  0.5,
		},
		"uncapped reports nothing": {
			files: map[string]string{"memory.current": "512\n", "memory.max": "max\n"},
			want:  0,
		},
		"over the limit clamps to one": {
			files: map[string]string{"memory.current": "2048\n", "memory.max": "1024\n"},
			want:  1,
		},
		"outside a cgroup reports nothing": {
			files: map[string]string{},
			want:  0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			usage, err := cgroupFixture(t, tc.files).MemoryUsage(nil)
			require.NoError(t, err)
			require.InDelta(t, tc.want, usage, 0.0001)
		})
	}
}

// CPU is a delta between calls, so the first one has nothing to compare against.
func TestCgroupCPUUsageNeedsTwoSamples(t *testing.T) {
	t.Parallel()

	info := cgroupFixture(t, map[string]string{
		"cpu.stat": "usage_usec 1000\nsystem_usec 10\n",
		"cpu.max":  "100000 100000\n",
	})

	first, err := info.CpuUsage(nil)
	require.NoError(t, err)
	require.Zero(t, first)

	// Same counter: no CPU was burned between the samples.
	idle, err := info.CpuUsage(nil)
	require.NoError(t, err)
	require.Zero(t, idle)
}

func TestCgroupCPUUsageClampsAtFullyBusy(t *testing.T) {
	t.Parallel()

	info := cgroupFixture(t, map[string]string{
		"cpu.stat": "usage_usec 1000\n",
		"cpu.max":  "100000 100000\n",
	})

	_, err := info.CpuUsage(nil)
	require.NoError(t, err)

	// Far more CPU time than wall time could allow, so the fraction clamps.
	require.NoError(t, os.WriteFile(filepath.Join(info.root, "cpu.stat"),
		[]byte("usage_usec 999999999999\n"), 0o600))

	busy, err := info.CpuUsage(nil)
	require.NoError(t, err)
	require.InDelta(t, 1.0, busy, 0.0001)
}

// A counter that went backwards means the cgroup was replaced, not that the
// worker used negative CPU.
func TestCgroupCPUUsageIgnoresACounterReset(t *testing.T) {
	t.Parallel()

	info := cgroupFixture(t, map[string]string{
		"cpu.stat": "usage_usec 5000\n",
		"cpu.max":  "100000 100000\n",
	})

	_, err := info.CpuUsage(nil)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(info.root, "cpu.stat"),
		[]byte("usage_usec 10\n"), 0o600))

	usage, err := info.CpuUsage(nil)
	require.NoError(t, err)
	require.Zero(t, usage)
}

// pyck sets no CPU limits, so cpu.max reads "max" on every worker. Falling back
// to the runtime's core count is what keeps CPU from reporting 0 forever.
func TestCgroupCPUUsageWithoutAQuota(t *testing.T) {
	t.Parallel()

	info := cgroupFixture(t, map[string]string{
		"cpu.stat": "usage_usec 1000\n",
		"cpu.max":  "max 100000\n",
	})

	cores, err := info.readCPUCores()
	require.NoError(t, err)
	require.InDelta(t, float64(runtime.GOMAXPROCS(0)), cores, 0.0001)

	_, err = info.CpuUsage(nil)
	require.NoError(t, err)

	// Far more CPU time than the cores could deliver, so the fraction clamps —
	// proving the delta is measured rather than short-circuited to 0.
	require.NoError(t, os.WriteFile(filepath.Join(info.root, "cpu.stat"),
		[]byte("usage_usec 999999999999\n"), 0o600))

	busy, err := info.CpuUsage(nil)
	require.NoError(t, err)
	require.InDelta(t, 1.0, busy, 0.0001)
}

// Outside a container there is no cpu.stat, so nothing is reported at all.
func TestCgroupCPUUsageOutsideACgroup(t *testing.T) {
	t.Parallel()

	usage, err := cgroupFixture(t, map[string]string{}).CpuUsage(nil)
	require.NoError(t, err)
	require.Zero(t, usage)
}

// Runs only under cgroup v2: proves the parsing matches the real files rather
// than the fixtures above.
func TestCgroupReadsRealFiles(t *testing.T) {
	t.Parallel()

	info := newCgroupInfo()
	if info.read("memory.max") == "" {
		t.Skip("not running under cgroup v2")
	}

	memory, err := info.MemoryUsage(nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, memory, 0.0)
	require.LessOrEqual(t, memory, 1.0)

	// An unlimited cgroup — a CI runner, say — has no fraction to report.
	limit, err := info.readUint("memory.max")
	require.NoError(t, err)

	if limit > 0 {
		require.Positive(t, memory, "a limited cgroup should report its share")
	}

	_, err = info.CpuUsage(nil)
	require.NoError(t, err)
}

// The SDK reads host CPU and memory for the heartbeat off the workflow task
// slot supplier, and only when it implements HasSysInfoProvider. Without that
// both report 0 and Temporal's UI shows no usage data for the worker.
func TestSetHostInfoTunerExposesSysInfoProvider(t *testing.T) {
	t.Parallel()

	var options temporalworker.Options
	require.NoError(t, setHostInfoTuner(&options))
	require.NotNil(t, options.Tuner)

	supplier := options.Tuner.GetWorkflowTaskSlotSupplier()
	has, ok := supplier.(temporalworker.HasSysInfoProvider)
	require.True(t, ok, "workflow slot supplier must implement HasSysInfoProvider")
	require.NotNil(t, has.SysInfoProvider())

	require.Equal(t, defaultWorkerSlots, supplier.MaxSlots(),
		"slot count must stay at the SDK default this tuner replaces")
}

// Slot suppliers hold a semaphore, so every task queue needs its own tuner —
// sharing one would pool their slots into a single budget.
func TestSetHostInfoTunerBuildsOnePerCall(t *testing.T) {
	t.Parallel()

	var first, second temporalworker.Options
	require.NoError(t, setHostInfoTuner(&first))
	require.NoError(t, setHostInfoTuner(&second))

	// The wrapper is a value, so compare the fixed-size supplier it delegates
	// to — that is what holds the semaphore.
	wrapped := func(options temporalworker.Options) temporalworker.SlotSupplier {
		supplier, ok := options.Tuner.GetWorkflowTaskSlotSupplier().(sysInfoSlotSupplier)
		require.True(t, ok)

		return supplier.SlotSupplier
	}

	require.NotSame(t, wrapped(first), wrapped(second))
}

func TestSetHostInfoTunerKeepsAnExistingTuner(t *testing.T) {
	t.Parallel()

	chosen, err := temporalworker.NewFixedSizeTuner(temporalworker.FixedSizeTunerOptions{NumWorkflowSlots: 7})
	require.NoError(t, err)

	options := temporalworker.Options{Tuner: chosen}
	require.NoError(t, setHostInfoTuner(&options))
	require.Equal(t, chosen, options.Tuner)
}

// The SDK panics when MaxConcurrent* fields coexist with a Tuner, so they have
// to reach the worker as slot counts instead.
func TestSetHostInfoTunerCarriesMaxConcurrentLimits(t *testing.T) {
	t.Parallel()

	options := temporalworker.Options{
		MaxConcurrentWorkflowTaskExecutionSize:  11,
		MaxConcurrentActivityExecutionSize:      200,
		MaxConcurrentLocalActivityExecutionSize: 33,
		MaxConcurrentNexusTaskExecutionSize:     44,
	}
	require.NoError(t, setHostInfoTuner(&options))

	require.Zero(t, options.MaxConcurrentWorkflowTaskExecutionSize)
	require.Zero(t, options.MaxConcurrentActivityExecutionSize)
	require.Zero(t, options.MaxConcurrentLocalActivityExecutionSize)
	require.Zero(t, options.MaxConcurrentNexusTaskExecutionSize)

	require.Equal(t, 11, options.Tuner.GetWorkflowTaskSlotSupplier().MaxSlots())
	require.Equal(t, 200, options.Tuner.GetActivityTaskSlotSupplier().MaxSlots())
	require.Equal(t, 33, options.Tuner.GetLocalActivitySlotSupplier().MaxSlots())
	require.Equal(t, 44, options.Tuner.GetNexusSlotSupplier().MaxSlots())

	// Sessions borrow the activity limit, as they do inside the SDK.
	require.Equal(t, 200, options.Tuner.GetSessionActivitySlotSupplier().MaxSlots())
}
