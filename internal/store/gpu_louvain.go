package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "embed"
)

//go:embed gpu_louvain.py
var gpuLouvainScript []byte

// gpuScriptOnce extracts gpu_louvain.py to a temp file exactly once.
var (
	gpuScriptOnce sync.Once
	gpuScriptPath string
	gpuScriptErr  error
)

// isGPUEnabled returns true when the CBM_GPU environment variable is set to "1".
func isGPUEnabled() bool {
	return os.Getenv("CBM_GPU") == "1"
}

// gpuScriptFile returns the path to the extracted gpu_louvain.py, extracting it
// on first call. The file persists for the process lifetime (temp dir cleanup
// is handled by the OS on exit).
func gpuScriptFile() (string, error) {
	gpuScriptOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cbm-gpu-*")
		if err != nil {
			gpuScriptErr = fmt.Errorf("create temp dir: %w", err)
			return
		}
		path := filepath.Join(dir, "gpu_louvain.py")
		if err := os.WriteFile(path, gpuLouvainScript, 0o600); err != nil {
			gpuScriptErr = fmt.Errorf("write gpu script: %w", err)
			return
		}
		gpuScriptPath = path
	})
	return gpuScriptPath, gpuScriptErr
}

// findPython returns the path to a usable Python 3 interpreter, or an error.
func findPython() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Python interpreter found in PATH")
}

// tryGPULouvain runs the GPU sidecar and returns the partition.
// Returns (nil, false) on any failure so the caller can fall back to Go Louvain.
func tryGPULouvain(nodes []int64, edges []LouvainEdge) (map[int64]int, bool) {
	scriptPath, err := gpuScriptFile()
	if err != nil {
		slog.Debug("gpu.louvain.script_err", "err", err)
		return nil, false
	}
	python, err := findPython()
	if err != nil {
		slog.Debug("gpu.louvain.no_python", "err", err)
		return nil, false
	}

	// Build stdin payload: header + node IDs + edge pairs
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d %d\n", len(nodes), len(edges))
	for _, id := range nodes {
		fmt.Fprintf(&sb, "%d\n", id)
	}
	for _, e := range edges {
		fmt.Fprintf(&sb, "%d %d\n", e.Src, e.Dst)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, python, scriptPath)
	cmd.Stdin = strings.NewReader(sb.String())
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			slog.Debug("gpu.louvain.exit", "code", exitErr.ExitCode(), "stderr", string(exitErr.Stderr))
		}
		return nil, false
	}

	// Parse stdout: "node_id community" per line
	partition := make(map[int64]int, len(nodes))
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			slog.Warn("gpu.louvain.parse_err", "line", line)
			return nil, false
		}
		nodeID, err1 := strconv.ParseInt(parts[0], 10, 64)
		comm, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return nil, false
		}
		partition[nodeID] = comm
	}
	if len(partition) != len(nodes) {
		slog.Warn("gpu.louvain.incomplete", "got", len(partition), "want", len(nodes))
		return nil, false
	}
	return partition, true
}
