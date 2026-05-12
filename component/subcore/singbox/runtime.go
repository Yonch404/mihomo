package singbox

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	ownerFileName  = "sing-box.owner"
)

type runtimeLayout struct {
	RunDir         string
	AssetDir       string
	ConfigPath     string
	PIDPath        string
	OwnerPath      string
	ExecutablePath string
	Assets         *assetSet
}

type pidRecord struct {
	PID            int    `json:"pid"`
	ParentPID      int    `json:"parent-pid"`
	ParentExe      string `json:"parent-executable,omitempty"`
	Executable     string `json:"executable"`
	ConfigPath     string `json:"config"`
	AssetHash      string `json:"asset-hash"`
	ConfigHash     string `json:"config-hash"`
	RunID          string `json:"run-id"`
	OwnerID        string `json:"owner-id,omitempty"`
	StartedUnixSec int64  `json:"started-unix-sec"`
}

type ownerRecord struct {
	PID            int    `json:"pid"`
	Executable     string `json:"executable,omitempty"`
	OwnerID        string `json:"owner-id"`
	StartedUnixSec int64  `json:"started-unix-sec"`
}

type ownerLock struct {
	path   string
	record ownerRecord
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
		OwnerPath:      filepath.Join(runDir, ownerFileName),
		ExecutablePath: executablePath,
		Assets:         assets,
	}, nil
}

func preparePIDRuntimeLayout() *runtimeLayout {
	runDir := filepath.Join(C.Path.HomeDir(), "run", subcoreName)
	return &runtimeLayout{
		RunDir:    runDir,
		PIDPath:   filepath.Join(runDir, pidFileName),
		OwnerPath: filepath.Join(runDir, ownerFileName),
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
		sameContent, err := l.sameAssetFileContent(target, file)
		if err != nil {
			return err
		}
		if sameContent {
			_ = os.Chmod(target, mode)
			continue
		}
		if err := l.writeAssetFile(target, file, mode); err != nil {
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

func writePIDFile(layout *runtimeLayout, pid int, cfgHash string, runID string, ownerID string) error {
	record := pidRecord{
		PID:            pid,
		ParentPID:      os.Getpid(),
		ParentExe:      currentExecutablePath(),
		Executable:     layout.ExecutablePath,
		ConfigPath:     layout.ConfigPath,
		AssetHash:      layout.Assets.Hash,
		ConfigHash:     cfgHash,
		RunID:          runID,
		OwnerID:        ownerID,
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

func reapOrphan(layout *runtimeLayout, failOnLiveOwner bool) error {
	record, err := readPIDFile(layout.PIDPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warnln("[sing-box] read pid file failed: %s", err.Error())
		}
		return nil
	}
	if ownedByLiveOtherManager(record) {
		message := fmt.Sprintf("sing-box is already managed by another mihomo process pid %d", record.ParentPID)
		if failOnLiveOwner {
			return errors.New(message)
		}
		log.Warnln("[sing-box] %s, skip orphan cleanup", message)
		return nil
	}
	if record.PID <= 0 {
		removePIDFile(layout.PIDPath)
		return nil
	}
	if !processExists(record.PID) {
		removePIDFile(layout.PIDPath)
		return nil
	}
	if !isManagedExecutablePath(record.Executable) {
		log.Warnln("[sing-box] stale pid file points outside managed directory, skip killing pid %d", record.PID)
		removePIDFile(layout.PIDPath)
		return nil
	}

	if runningPath, err := processPath(record.PID); err == nil {
		if !samePath(runningPath, record.Executable) {
			log.Warnln("[sing-box] stale pid file pid %d now belongs to another executable, skip cleanup", record.PID)
			removePIDFile(layout.PIDPath)
			return nil
		}
	}

	log.Warnln("[sing-box] cleanup orphan process pid %d", record.PID)
	_ = terminatePID(record.PID)
	if !waitProcessExit(record.PID, 5*time.Second) {
		_ = killPID(record.PID)
		_ = waitProcessExit(record.PID, 2*time.Second)
	}
	removePIDFile(layout.PIDPath)
	return nil
}

func acquireOwnerLock(layout *runtimeLayout) (*ownerLock, error) {
	if err := os.MkdirAll(layout.RunDir, 0o700); err != nil {
		return nil, err
	}

	record := ownerRecord{
		PID:            os.Getpid(),
		Executable:     currentExecutablePath(),
		OwnerID:        newRunID(),
		StartedUnixSec: time.Now().Unix(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt < 10; attempt++ {
		file, err := os.OpenFile(layout.OwnerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil {
				_ = os.Remove(layout.OwnerPath)
				return nil, writeErr
			}
			if closeErr != nil {
				_ = os.Remove(layout.OwnerPath)
				return nil, closeErr
			}
			return &ownerLock{path: layout.OwnerPath, record: record}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}

		owner, err := readOwnerFile(layout.OwnerPath)
		if err != nil {
			if attempt < 4 {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			log.Warnln("[sing-box] remove invalid owner lock: %s", err.Error())
			if err := os.Remove(layout.OwnerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("remove invalid sing-box owner lock: %w", err)
			}
			continue
		}
		if ownerRecordAlive(owner) {
			return nil, fmt.Errorf("sing-box is already managed by another mihomo process pid %d", owner.PID)
		}
		if err := os.Remove(layout.OwnerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, errors.New("acquire sing-box owner lock failed")
}

func readOwnerFile(path string) (*ownerRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	record := &ownerRecord{}
	if err := json.Unmarshal(data, record); err != nil {
		return nil, err
	}
	return record, nil
}

func (l *ownerLock) ID() string {
	if l == nil {
		return ""
	}
	return l.record.OwnerID
}

func (l *ownerLock) Release() {
	if l == nil || l.path == "" {
		return
	}
	record, err := readOwnerFile(l.path)
	if err != nil {
		return
	}
	if record.PID == l.record.PID && record.OwnerID == l.record.OwnerID {
		_ = os.Remove(l.path)
	}
}

func ownedByLiveOtherManager(record *pidRecord) bool {
	if record == nil || record.ParentPID <= 0 || record.ParentPID == os.Getpid() {
		return false
	}
	if !processExists(record.ParentPID) {
		return false
	}
	if record.ParentExe != "" {
		if runningPath, err := processPath(record.ParentPID); err == nil && !samePath(runningPath, record.ParentExe) {
			return false
		}
	}
	return true
}

func ownerRecordAlive(record *ownerRecord) bool {
	if record == nil || record.PID <= 0 || record.PID == os.Getpid() {
		return false
	}
	if !processExists(record.PID) {
		return false
	}
	if record.Executable != "" {
		if runningPath, err := processPath(record.PID); err == nil && !samePath(runningPath, record.Executable) {
			return false
		}
	}
	return true
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

func cleanupOldAssets(layout *runtimeLayout) {
	if err := removeOldAssets(layout); err != nil {
		log.Warnln("[sing-box] cleanup old assets failed: %s", err.Error())
	}
}

func removeOldAssets(layout *runtimeLayout) error {
	if layout == nil || layout.AssetDir == "" {
		return nil
	}
	assetRoot := filepath.Dir(layout.AssetDir)
	entries, err := os.ReadDir(assetRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	activeAssetDir := activeAssetDirFromPID(layout.PIDPath)
	prefix := runtime.GOOS + "-" + runtime.GOARCH + "-"
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if !filepath.IsLocal(entry.Name()) {
			continue
		}
		target := filepath.Join(assetRoot, entry.Name())
		if err := ensureSubpath(assetRoot, target); err != nil {
			return err
		}
		if samePath(target, layout.AssetDir) || (activeAssetDir != "" && samePath(target, activeAssetDir)) {
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove %s: %w", target, err)
		}
	}
	return nil
}

func activeAssetDirFromPID(pidPath string) string {
	record, err := readPIDFile(pidPath)
	if err != nil || record.PID <= 0 || record.Executable == "" || !processExists(record.PID) {
		return ""
	}
	return cleanComparablePath(filepath.Dir(record.Executable))
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

func (l *runtimeLayout) sameAssetFileContent(target string, file assetFile) (bool, error) {
	old, err := os.Open(target)
	if err != nil {
		return false, nil
	}
	defer old.Close()

	source, err := l.Assets.Open(file)
	if err != nil {
		return false, err
	}
	defer source.Close()

	return sameReaderContent(old, source)
}

func (l *runtimeLayout) writeAssetFile(target string, file assetFile, mode os.FileMode) (err error) {
	source, err := l.Assets.Open(file)
	if err != nil {
		return err
	}
	defer source.Close()

	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := output.Close()
		if closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	if _, err := io.Copy(output, source); err != nil {
		return err
	}
	return output.Chmod(mode)
}

func sameReaderContent(left io.Reader, right io.Reader) (bool, error) {
	leftHash := sha256.New()
	if _, err := io.Copy(leftHash, left); err != nil {
		return false, err
	}

	rightHash := sha256.New()
	if _, err := io.Copy(rightHash, right); err != nil {
		return false, err
	}

	return bytes.Equal(leftHash.Sum(nil), rightHash.Sum(nil)), nil
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

func currentExecutablePath() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return cleanComparablePath(path)
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
