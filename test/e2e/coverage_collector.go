package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

func collectDiskmakerCoverage(namespace string) {
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
		fmt.Fprintf(GinkgoWriter, "coverage: no diskmaker pods found, skipping collection\n")
		return
	}

	fmt.Fprintf(GinkgoWriter, "coverage: signaling %d diskmaker pod(s) for coverage flush\n", len(allPods))
	for _, pod := range allPods {
		findAndSignal := `for f in /proc/[0-9]*/cmdline; do ` +
			`cmd=$(cat "$f" 2>/dev/null | tr '\0' ' '); ` +
			`case "$cmd" in /usr/bin/diskmaker*) pid=${f#/proc/}; pid=${pid%/cmdline}; ` +
			`kill -USR1 "$pid" && echo "signaled pid $pid"; break;; esac; done`
		cmd := exec.Command("oc", "exec", "-n", namespace, pod, "--", "bash", "-c", findAndSignal)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(GinkgoWriter, "coverage: failed to signal pod %s: %v (%s)\n", pod, err, strings.TrimSpace(string(out)))
		} else {
			fmt.Fprintf(GinkgoWriter, "coverage: %s: %s\n", pod, strings.TrimSpace(string(out)))
		}
	}

	time.Sleep(3 * time.Second)

	for _, pod := range allPods {
		dest := filepath.Join(outputDir, pod)
		if err := os.MkdirAll(dest, 0755); err != nil {
			fmt.Fprintf(GinkgoWriter, "coverage: failed to create dir %s: %v\n", dest, err)
			continue
		}
		src := fmt.Sprintf("%s/%s:%s", namespace, pod, coverDir)
		cmd := exec.Command("oc", "cp", src, dest)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(GinkgoWriter, "coverage: failed to copy from pod %s: %v (%s)\n", pod, err, strings.TrimSpace(string(out)))
		} else {
			fmt.Fprintf(GinkgoWriter, "coverage: collected data from pod %s\n", pod)
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
