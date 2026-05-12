package singbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/metacubex/mihomo/log"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	switch os.Getenv("MIHOMO_SINGBOX_TEST_HELPER") {
	case "exit":
		os.Exit(0)
	case "sleep":
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartProcessQuickExitIsObservedAfterManagerOwnsState(t *testing.T) {
	t.Setenv("MIHOMO_SINGBOX_TEST_HELPER", "exit")
	layout := newTestRuntimeLayout(t)

	state, err := startProcess(layout, "config-hash")
	require.NoError(t, err)

	m := &manager{}
	m.mux.Lock()
	m.current = state
	require.NoError(t, writePIDFile(layout, state.pid, state.configHash, state.runID, "owner-id"))
	m.watchProcess(state)
	m.mux.Unlock()

	require.Eventually(t, func() bool {
		m.mux.Lock()
		defer m.mux.Unlock()
		return m.current == nil && m.failureCount == 1
	}, 2*time.Second, 10*time.Millisecond)

	_, err = os.Stat(layout.PIDPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestReapOrphanSkipsLiveOtherManager(t *testing.T) {
	layout := newTestRuntimeLayout(t)
	other := startHelperProcess(t, "sleep")

	record := pidRecord{
		PID:        1,
		ParentPID:  other.Process.Pid,
		ParentExe:  currentExecutablePath(),
		Executable: filepath.Join(layout.AssetDir, executableName()),
	}
	data, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(layout.PIDPath, data, 0o600))

	require.NoError(t, reapOrphan(layout, false))
	require.FileExists(t, layout.PIDPath)

	err = reapOrphan(layout, true)
	require.ErrorContains(t, err, "already managed by another mihomo process")
}

func TestAcquireOwnerLockRejectsLiveOtherOwnerAndReclaimsStaleLock(t *testing.T) {
	layout := newTestRuntimeLayout(t)
	other := startHelperProcess(t, "sleep")

	record := ownerRecord{
		PID:            other.Process.Pid,
		Executable:     currentExecutablePath(),
		OwnerID:        "other-owner",
		StartedUnixSec: time.Now().Unix(),
	}
	data, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(layout.OwnerPath, data, 0o600))

	_, err = acquireOwnerLock(layout)
	require.ErrorContains(t, err, "already managed by another mihomo process")

	stopHelperProcess(other)

	lock, err := acquireOwnerLock(layout)
	require.NoError(t, err)
	lock.Release()
	_, err = os.Stat(layout.OwnerPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRemoveOldAssetsKeepsCurrentAndOtherPlatforms(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subcores", subcoreName)
	prefix := runtime.GOOS + "-" + runtime.GOARCH + "-"
	current := filepath.Join(root, prefix+"current")
	old := filepath.Join(root, prefix+"old")
	foreign := filepath.Join(root, "foreign-platform-old")

	require.NoError(t, os.MkdirAll(current, 0o700))
	require.NoError(t, os.MkdirAll(old, 0o700))
	require.NoError(t, os.MkdirAll(foreign, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(old, "marker"), []byte("old"), 0o600))

	layout := &runtimeLayout{
		AssetDir: current,
		PIDPath:  filepath.Join(t.TempDir(), pidFileName),
	}
	require.NoError(t, removeOldAssets(layout))

	require.DirExists(t, current)
	require.NoDirExists(t, old)
	require.DirExists(t, foreign)
}

func TestParseForwardedLogLineStripsANSIAndDetectsTextLevel(t *testing.T) {
	line := "\x1b[36mINFO\x1b[0m[0076] [\x1b[38;5;224m3696673744\x1b[0m 0ms] inbound/socks[socks-in-a02]: inbound connection to api.github.com:443"

	level, message, ok := parseForwardedLogLine(line, log.WARNING)

	require.True(t, ok)
	require.Equal(t, log.INFO, level)
	require.NotContains(t, message, "\x1b")
	require.Equal(t, "[3696673744 0ms] inbound/socks[socks-in-a02]: inbound connection to api.github.com:443", message)
}

func TestParseForwardedLogLineRemovesSingBoxTextHeader(t *testing.T) {
	line := "WARN[0001] dns: lookup example.com failed"

	level, message, ok := parseForwardedLogLine(line, log.INFO)

	require.True(t, ok)
	require.Equal(t, log.WARNING, level)
	require.Equal(t, "dns: lookup example.com failed", message)
}

func TestParseForwardedLogLineDetectsStructuredLevelAfterANSIStrip(t *testing.T) {
	line := "\x1b[32m{\"level\":\"error\",\"message\":\"dial failed\"}\x1b[0m"

	level, message, ok := parseForwardedLogLine(line, log.WARNING)

	require.True(t, ok)
	require.Equal(t, log.ERROR, level)
	require.Equal(t, "dial failed", message)
}

func newTestRuntimeLayout(t *testing.T) *runtimeLayout {
	t.Helper()

	root := t.TempDir()
	runDir := filepath.Join(root, "run", subcoreName)
	assetDir := filepath.Join(root, "subcores", subcoreName, runtime.GOOS+"-"+runtime.GOARCH+"-test")
	require.NoError(t, os.MkdirAll(runDir, 0o700))
	require.NoError(t, os.MkdirAll(assetDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, configFileName), []byte(`{"log":{"level":"info"}}`), 0o600))

	return &runtimeLayout{
		RunDir:         runDir,
		AssetDir:       assetDir,
		ConfigPath:     filepath.Join(runDir, configFileName),
		PIDPath:        filepath.Join(runDir, pidFileName),
		OwnerPath:      filepath.Join(runDir, ownerFileName),
		ExecutablePath: currentExecutablePath(),
		Assets: &assetSet{
			Hash:       "test-asset-hash",
			Executable: executableName(),
		},
	}
}

func startHelperProcess(t *testing.T, mode string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "MIHOMO_SINGBOX_TEST_HELPER="+mode)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		stopHelperProcess(cmd)
	})
	return cmd
}

func stopHelperProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
