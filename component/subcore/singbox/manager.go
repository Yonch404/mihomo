package singbox

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/log"
)

const (
	stopTimeout        = 5 * time.Second
	killTimeout        = 2 * time.Second
	stableRunThreshold = 30 * time.Second
	maxQuickFailures   = 5
)

var defaultManager = &manager{}

type manager struct {
	mux          sync.Mutex
	current      *processState
	desired      *Config
	owner        *ownerLock
	failureCount int
}

type processState struct {
	cmd        *exec.Cmd
	handle     *managedProcess
	done       chan error
	stdout     io.Reader
	stderr     io.Reader
	layout     *runtimeLayout
	configHash string
	runID      string
	pid        int
	startedAt  time.Time
}

func Apply(cfg *Config) error {
	return defaultManager.Apply(cfg)
}

func Shutdown() {
	defaultManager.Shutdown()
}

func Available() bool {
	return embeddedAvailable()
}

func (m *manager) Apply(cfg *Config) (retErr error) {
	m.mux.Lock()
	defer m.mux.Unlock()

	cfg = cfg.Clone()
	if cfg == nil {
		m.desired = nil
		m.failureCount = 0
		m.stopCurrentLocked()
		if err := reapOrphan(preparePIDRuntimeLayout(), false); err != nil {
			log.Warnln("[sing-box] cleanup orphan failed: %s", err.Error())
		}
		m.releaseOwnerLocked()
		return nil
	}

	if m.current != nil && m.current.configHash == cfg.Hash {
		m.desired = cfg
		return nil
	}

	layout, err := prepareRuntimeLayout()
	if err != nil {
		return err
	}
	if err := m.ensureOwnerLocked(layout); err != nil {
		return err
	}
	defer func() {
		if retErr != nil && m.current == nil {
			m.releaseOwnerLocked()
		}
	}()

	if m.current == nil {
		if err := reapOrphan(layout, true); err != nil {
			return err
		}
	}

	if err := layout.Ensure(); err != nil {
		return err
	}
	if err := layout.WriteConfig(cfg); err != nil {
		return err
	}
	if err := checkConfig(layout); err != nil {
		return err
	}

	if m.desired == nil || m.desired.Hash != cfg.Hash {
		m.failureCount = 0
	}
	m.desired = cfg
	m.stopCurrentLocked()

	state, err := startProcess(layout, cfg.Hash)
	if err != nil {
		return err
	}
	m.current = state
	if err := writePIDFile(layout, state.pid, cfg.Hash, state.runID, m.owner.ID()); err != nil {
		m.current = nil
		stopUnmonitoredProcess(state)
		return err
	}
	m.watchProcess(state)
	cleanupOldAssets(layout)
	log.Infoln("[sing-box] started, pid: %d", state.pid)
	return nil
}

func (m *manager) Shutdown() {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.desired = nil
	m.stopCurrentLocked()
	m.releaseOwnerLocked()
}

func (m *manager) stopCurrentLocked() {
	if m.current == nil {
		return
	}
	state := m.current
	m.current = nil
	stopProcess(state)
	removePIDFile(state.layout.PIDPath)
}

func (m *manager) ensureOwnerLocked(layout *runtimeLayout) error {
	if m.owner != nil {
		return nil
	}
	owner, err := acquireOwnerLock(layout)
	if err != nil {
		return err
	}
	m.owner = owner
	return nil
}

func (m *manager) releaseOwnerLocked() {
	if m.owner == nil {
		return
	}
	m.owner.Release()
	m.owner = nil
}

func checkConfig(layout *runtimeLayout) error {
	cmd := exec.Command(layout.ExecutablePath, "check", "-c", layout.ConfigPath)
	cmd.Dir = layout.AssetDir
	cmd.Env = layout.CommandEnv("check")
	prepareCheckCommand(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sing-box config check failed: %w, %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func startProcess(layout *runtimeLayout, configHash string) (*processState, error) {
	runID := newRunID()
	cmd := exec.Command(layout.ExecutablePath, "run", "-c", layout.ConfigPath)
	cmd.Dir = layout.AssetDir
	cmd.Env = layout.CommandEnv(runID)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	handle, err := prepareStartCommand(cmd)
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cleanupManagedProcess(handle)
		return nil, err
	}
	if err := afterStartCommand(handle, cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cleanupManagedProcess(handle)
		return nil, err
	}

	state := &processState{
		cmd:        cmd,
		handle:     handle,
		done:       make(chan error, 1),
		layout:     layout,
		configHash: configHash,
		runID:      runID,
		pid:        cmd.Process.Pid,
		startedAt:  time.Now(),
		stdout:     stdout,
		stderr:     stderr,
	}

	return state, nil
}

func (m *manager) watchProcess(state *processState) {
	go forwardLogs(state.stdout, log.INFO)
	go forwardLogs(state.stderr, log.WARNING)
	go m.waitProcess(state)
}

func stopProcess(state *processState) {
	_ = terminateCommand(state.cmd, state.handle)
	select {
	case <-state.done:
	case <-time.After(stopTimeout):
		_ = killCommand(state.cmd, state.handle)
		select {
		case <-state.done:
		case <-time.After(killTimeout):
		}
	}
	cleanupManagedProcess(state.handle)
}

func stopUnmonitoredProcess(state *processState) {
	wait := make(chan error, 1)
	go func() {
		wait <- state.cmd.Wait()
	}()

	_ = terminateCommand(state.cmd, state.handle)
	select {
	case <-wait:
	case <-time.After(stopTimeout):
		_ = killCommand(state.cmd, state.handle)
		select {
		case <-wait:
		case <-time.After(killTimeout):
		}
	}
	cleanupManagedProcess(state.handle)
}

func (m *manager) waitProcess(state *processState) {
	err := state.cmd.Wait()
	state.done <- err
	close(state.done)
	cleanupManagedProcess(state.handle)

	m.mux.Lock()
	defer m.mux.Unlock()

	if m.current != state {
		return
	}
	m.current = nil
	removePIDFile(state.layout.PIDPath)

	uptime := time.Since(state.startedAt)
	if uptime > stableRunThreshold {
		m.failureCount = 0
	}
	m.failureCount++
	if m.failureCount > maxQuickFailures {
		log.Errorln("[sing-box] exited too often, stop automatic restart: %v", err)
		return
	}

	delay := restartDelay(m.failureCount)
	log.Warnln("[sing-box] exited: %v, restart in %s", err, delay)
	if m.desired != nil && m.desired.Hash == state.configHash {
		cfg := m.desired.Clone()
		go m.restartAfter(delay, cfg)
	}
}

func (m *manager) restartAfter(delay time.Duration, cfg *Config) {
	time.Sleep(delay)
	if err := m.Apply(cfg); err != nil {
		log.Errorln("[sing-box] restart failed: %s", err.Error())
	}
}

func restartDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := time.Duration(1<<(failures-1)) * time.Second
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func forwardLogs(reader io.Reader, fallbackLevel log.LogLevel) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		forwardLogLine(scanner.Text(), fallbackLevel)
	}
	if err := scanner.Err(); err != nil {
		log.Debugln("[sing-box] read log failed: %s", err.Error())
	}
}

type structuredLog struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Msg     string `json:"msg"`
}

func forwardLogLine(line string, fallbackLevel log.LogLevel) {
	level, message, ok := parseForwardedLogLine(line, fallbackLevel)
	if !ok {
		return
	}

	writeLog(level, "[sing-box] %s", message)
}

func parseForwardedLogLine(line string, fallbackLevel log.LogLevel) (log.LogLevel, string, bool) {
	line = strings.TrimSpace(stripANSIEscapeSequences(line))
	if line == "" {
		return fallbackLevel, "", false
	}

	level := fallbackLevel
	message := line
	if strings.HasPrefix(line, "{") {
		var item structuredLog
		if err := json.Unmarshal([]byte(line), &item); err == nil {
			if parsed, ok := parseLogLevel(item.Level); ok {
				level = parsed
			}
			if item.Message != "" {
				message = item.Message
			} else if item.Msg != "" {
				message = item.Msg
			}
		}
	} else if parsed, parsedMessage, ok := parseTextLogLine(line); ok {
		level = parsed
		message = parsedMessage
	}

	return level, message, true
}

func stripANSIEscapeSequences(line string) string {
	var builder strings.Builder
	builder.Grow(len(line))

	for i := 0; i < len(line); i++ {
		character := line[i]
		if character == 0x1b {
			if i+1 >= len(line) {
				continue
			}
			switch line[i+1] {
			case '[':
				i += 2
				for i < len(line) {
					if line[i] >= 0x40 && line[i] <= 0x7e {
						break
					}
					i++
				}
			case ']':
				i += 2
				for i < len(line) {
					if line[i] == 0x07 {
						break
					}
					if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '\\' {
						i++
						break
					}
					i++
				}
			default:
				i++
			}
			continue
		}
		if (character < 0x20 && character != '\t') || character == 0x7f {
			continue
		}
		builder.WriteByte(character)
	}

	return builder.String()
}

func parseTextLogLine(line string) (log.LogLevel, string, bool) {
	line = strings.TrimLeft(line, " \t")
	end := 0
	for end < len(line) {
		character := line[end]
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
			break
		}
		end++
	}
	if end == 0 {
		return log.INFO, "", false
	}
	level, ok := parseLogLevel(line[:end])
	if !ok {
		return log.INFO, "", false
	}

	message := strings.TrimLeft(line[end:], " \t")
	if strings.HasPrefix(message, "[") {
		if closeBracket := strings.IndexByte(message, ']'); closeBracket >= 0 {
			message = strings.TrimLeft(message[closeBracket+1:], " \t")
		}
	}
	message = strings.TrimPrefix(message, ":")
	message = strings.TrimLeft(message, " \t")
	if message == "" {
		message = strings.TrimSpace(line)
	}

	return level, message, true
}

func parseLogLevel(level string) (log.LogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "trace":
		return log.DEBUG, true
	case "info":
		return log.INFO, true
	case "warn", "warning":
		return log.WARNING, true
	case "error", "fatal", "panic":
		return log.ERROR, true
	default:
		return log.INFO, false
	}
}

func writeLog(level log.LogLevel, format string, args ...any) {
	switch level {
	case log.DEBUG:
		log.Debugln(format, args...)
	case log.WARNING:
		log.Warnln(format, args...)
	case log.ERROR:
		log.Errorln(format, args...)
	default:
		log.Infoln(format, args...)
	}
}
