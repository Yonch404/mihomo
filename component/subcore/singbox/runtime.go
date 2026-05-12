package singbox

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
)

const (
	subcoreName    = "sing-box"
	configFileName = "config.json"
	pidFileName    = "sing-box.pid"
)

type runtimeLayout struct {
	RunDir         string
	AssetDir       string
	ConfigPath     string
	PIDPath        string
	ExecutablePath string
	Assets         *assetSet
}

type pidRecord struct {
	PID            int    `json:"pid"`
	ParentPID      int    `json:"parent-pid"`
	Executable     string `json:"executable"`
	ConfigPath     string `json:"config"`
	AssetHash      string `json:"asset-hash"`
	ConfigHash     string `json:"config-hash"`
	RunID          string `json:"run-id"`
	StartedUnixSec int64  `json:"started-unix-sec"`
}

func prepareRuntimeLayout() (*runtimeLayout, error) {
	assets, err := loadEmbeddedAssets()
	if err != nil {
		return nil, err
	}
	if assets.Executable == "" {
		return nil, ErrAssetsNotEmbedded
	}

	assetDirName := runtime.GOOS + "-" + runtime.GOARCH + "-" + shortHash(assets.Hash)
	homeDir := C.Path.HomeDir()
	runDir := filepath.Join(homeDir, "run", subcoreName)
	assetDir := filepath.Join(homeDir, "subcores", subcoreName, assetDirName)
	executablePath := filepath.Join(assetDir, assets.Executable)

	return &runtimeLayout{
		RunDir:         runDir,
		AssetDir:       assetDir,
		ConfigPath:     filepath.Join(runDir, configFileName),
		PIDPath:        filepath.Join(runDir, pidFileName),
		ExecutablePath: executablePath,
		Assets:         assets,
	}, nil
}

func preparePIDRuntimeLayout() *runtimeLayout {
	runDir := filepath.Join(C.Path.HomeDir(), "run", subcoreName)
	return &runtimeLayout{
		RunDir:  runDir,
		PIDPath: filepath.Join(runDir, pidFileName),
	}
}

func (l *runtimeLayout) Ensure() error {
	if err := os.MkdirAll(l.RunDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(l.AssetDir, 0o700); err != nil {
		return err
	}

	for _, file := range l.Assets.Files {
		if !filepath.IsLocal(toLocalPath(file.Name)) {
			return fmt.Errorf("sing-box asset path is not local: %s", file.Name)
		}
		target := filepath.Join(l.AssetDir, toLocalPath(file.Name))
		if err := ensureSubpath(l.AssetDir, target); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}

		mode := os.FileMode(0o644)
		if filepath.Base(target) == executableName() {
			mode = 0o755
		}
		if sameFileContent(target, file.Data) {
			_ = os.Chmod(target, mode)
			continue
		}
		if err := os.WriteFile(target, file.Data, mode); err != nil {
			return err
		}
	}
	return nil
}

func (l *runtimeLayout) WriteConfig(cfg *Config) error {
	if err := os.MkdirAll(l.RunDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(l.ConfigPath, cfg.RawJSON, 0o600)
}

func (l *runtimeLayout) CommandEnv(runID string) []string {
	env := os.Environ()
	env = append(env,
		"MIHOMO_SINGBOX_MANAGED=1",
		"MIHOMO_SINGBOX_RUN_ID="+runID,
		"MIHOMO_SINGBOX_PARENT_PID="+fmt.Sprint(os.Getpid()),
		"MIHOMO_SINGBOX_ASSET_DIR="+l.AssetDir,
	)

	switch runtime.GOOS {
	case "windows":
		env = prependEnvPath(env, "PATH", l.AssetDir)
	case "darwin":
		env = prependEnvPath(env, "DYLD_LIBRARY_PATH", l.AssetDir)
	default:
		env = prependEnvPath(env, "LD_LIBRARY_PATH", l.AssetDir)
	}
	return env
}

func writePIDFile(layout *runtimeLayout, pid int, cfgHash string, runID string) error {
	record := pidRecord{
		PID:            pid,
		ParentPID:      os.Getpid(),
		Executable:     layout.ExecutablePath,
		ConfigPath:     layout.ConfigPath,
		AssetHash:      layout.Assets.Hash,
		ConfigHash:     cfgHash,
		RunID:          runID,
		StartedUnixSec: time.Now().Unix(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(layout.PIDPath, data, 0o600)
}

func removePIDFile(path string) {
	_ = os.Remove(path)
}

func reapOrphan(layout *runtimeLayout) {
	record, err := readPIDFile(layout.PIDPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warnln("[sing-box] read pid file failed: %s", err.Error())
		}
		return
	}
	if record.PID <= 0 {
		removePIDFile(layout.PIDPath)
		return
	}
	if !processExists(record.PID) {
		removePIDFile(layout.PIDPath)
		return
	}
	if !isManagedExecutablePath(record.Executable) {
		log.Warnln("[sing-box] stale pid file points outside managed directory, skip killing pid %d", record.PID)
		removePIDFile(layout.PIDPath)
		return
	}

	if runningPath, err := processPath(record.PID); err == nil {
		if !samePath(runningPath, record.Executable) {
			log.Warnln("[sing-box] stale pid file pid %d now belongs to another executable, skip cleanup", record.PID)
			removePIDFile(layout.PIDPath)
			return
		}
	}

	log.Warnln("[sing-box] cleanup orphan process pid %d", record.PID)
	_ = terminatePID(record.PID)
	if !waitProcessExit(record.PID, 5*time.Second) {
		_ = killPID(record.PID)
		_ = waitProcessExit(record.PID, 2*time.Second)
	}
	removePIDFile(layout.PIDPath)
}

func readPIDFile(path string) (*pidRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	record := &pidRecord{}
	if err := json.Unmarshal(data, record); err != nil {
		return nil, err
	}
	return record, nil
}

func waitProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !processExists(pid)
}

func ensureSubpath(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if !filepath.IsLocal(rel) {
		return fmt.Errorf("path is outside sing-box runtime directory: %s", target)
	}
	return nil
}

func sameFileContent(path string, data []byte) bool {
	old, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	oldHash := sha256.Sum256(old)
	newHash := sha256.Sum256(data)
	return oldHash == newHash
}

func shortHash(hash string) string {
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

func newRunID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return fmt.Sprint(time.Now().UnixNano())
}

func prependEnvPath(env []string, key, value string) []string {
	for index, item := range env {
		name, oldValue, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(name, key) {
			env[index] = name + "=" + value + string(os.PathListSeparator) + oldValue
			return env
		}
	}
	return append(env, key+"="+value)
}

func isManagedExecutablePath(path string) bool {
	if path == "" {
		return false
	}
	root := filepath.Join(C.Path.HomeDir(), "subcores", subcoreName)
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

func samePath(left, right string) bool {
	left = cleanComparablePath(left)
	right = cleanComparablePath(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func cleanComparablePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}
