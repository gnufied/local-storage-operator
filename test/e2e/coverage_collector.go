package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func collectDiskmakerCoverage(t *testing.T, namespace string) {
	outputDir := os.Getenv("COVER_OUTPUT_DIR")
	if outputDir == "" {
		outputDir = "_output/coverage"
	}

	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		return
	}

	coverDir := "/var/run/coverage"

	labels := []string{"app=diskmaker-manager", "app=diskmaker-discovery"}
	var allPods []string

	for _, label := range labels {
		pods := getPodsForLabel(namespace, label)
		allPods = append(allPods, pods...)
	}

	if len(allPods) == 0 {
		t.Log("coverage: no diskmaker pods found, skipping collection")
		return
	}

	t.Logf("coverage: signaling %d diskmaker pod(s) for coverage flush", len(allPods))
	for _, pod := range allPods {
		// Diskmaker pods run with hostPID, so PID 1 is the host's systemd,
		// not the diskmaker process. Find the actual PID by scanning /proc.
		findAndSignal := `for f in /proc/[0-9]*/cmdline; do ` +
			`cmd=$(cat "$f" 2>/dev/null | tr '\0' ' '); ` +
			`case "$cmd" in /usr/bin/diskmaker*) pid=${f#/proc/}; pid=${pid%/cmdline}; ` +
			`kill -USR1 "$pid" && echo "signaled pid $pid"; break;; esac; done`
		cmd := exec.Command("oc", "exec", "-n", namespace, pod, "--", "bash", "-c", findAndSignal)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("coverage: failed to signal pod %s: %v (%s)", pod, err, strings.TrimSpace(string(out)))
		} else {
			t.Logf("coverage: %s: %s", pod, strings.TrimSpace(string(out)))
		}
	}

	time.Sleep(3 * time.Second)

	for _, pod := range allPods {
		dest := filepath.Join(outputDir, pod)
		if err := os.MkdirAll(dest, 0755); err != nil {
			t.Logf("coverage: failed to create dir %s: %v", dest, err)
			continue
		}
		src := fmt.Sprintf("%s/%s:%s", namespace, pod, coverDir)
		cmd := exec.Command("oc", "cp", src, dest)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("coverage: failed to copy from pod %s: %v (%s)", pod, err, strings.TrimSpace(string(out)))
		} else {
			t.Logf("coverage: collected data from pod %s", pod)
		}
	}
}

func getPodsForLabel(namespace, label string) []string {
	cmd := exec.Command("oc", "get", "pods", "-n", namespace, "-l", label,
		"--field-selector=status.phase=Running",
		"-o", "jsonpath={.items[*].metadata.name}")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Fields(raw)
}
