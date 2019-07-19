package diskmaker

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	localv1 "github.com/openshift/local-storage-operator/pkg/apis/local/v1"
	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog"
)

// DiskMaker is a small utility that reads configmap and
// creates and symlinks disks in location from which local-storage-provisioner can access.
// It also ensures that only stable device names are used.

var (
	checkDuration = 5 * time.Second
	diskByIDPath  = "/dev/disk/by-id/*"
	rootfsDir     = "/rootfs"
)

type DiskMaker struct {
	configLocation  string
	symlinkLocation string
	apiClient       apiUpdater
	localVolume     *localv1.LocalVolume
	eventSync       *eventReporter
}

type DiskLocation struct {
	// diskNamePath stores full device name path - "/dev/sda"
	diskNamePath  string
	diskID        string
	directoryPath string
}

// DiskMaker returns a new instance of DiskMaker
func NewDiskMaker(configLocation, symLinkLocation string) *DiskMaker {
	t := &DiskMaker{}
	t.configLocation = configLocation
	t.symlinkLocation = symLinkLocation
	t.apiClient = newAPIUpdater()
	t.eventSync = newEventReporter(t.apiClient)
	return t
}

func (d *DiskMaker) loadConfig() (*DiskConfig, error) {
	var err error
	content, err := ioutil.ReadFile(d.configLocation)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %v", d.configLocation, err)
	}
	var diskConfig DiskConfig
	err = yaml.Unmarshal(content, &diskConfig)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling %s: %v", d.configLocation, err)
	}

	lv := &localv1.LocalVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diskConfig.OwnerName,
			Namespace: diskConfig.OwnerNamespace,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       diskConfig.OwnerKind,
			APIVersion: diskConfig.OwnerAPIVersion,
		},
	}
	lv, err = d.apiClient.getLocalVolume(lv)

	localKey := fmt.Sprintf("%s/%s", diskConfig.OwnerNamespace, diskConfig.OwnerName)
	if err != nil {
		return nil, fmt.Errorf("error fetching local volume %s: %v", localKey, err)
	}
	d.localVolume = lv

	return &diskConfig, nil
}

// Run and create disk config
func (d *DiskMaker) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(checkDuration)
	defer ticker.Stop()

	err := os.MkdirAll(d.symlinkLocation, 0755)
	if err != nil {
		klog.Errorf("error creating local-storage directory %s: %v", d.symlinkLocation, err)
		os.Exit(-1)
	}

	for {
		select {
		case <-ticker.C:
			diskConfig, err := d.loadConfig()
			if err != nil {
				klog.Errorf("error loading configuration: %v", err)
				break
			}
			d.symLinkDisks(diskConfig)
		case <-stop:
			klog.Infof("exiting, received message on stop channel")
			os.Exit(0)
		}
	}
}

func (d *DiskMaker) symLinkDisks(diskConfig *DiskConfig) {
	cmd := exec.Command("lsblk", "--list", "-o", "NAME,MOUNTPOINT", "--noheadings")
	var out bytes.Buffer
	var err error
	cmd.Stdout = &out
	err = cmd.Run()
	if err != nil {
		msg := fmt.Sprintf("error running lsblk: %v", err)
		e := newEvent(ErrorRunningBlockList, msg, "")
		d.eventSync.report(e, d.localVolume)
		klog.Errorf(msg)
		return
	}
	deviceSet, err := d.findNewDisks(out.String())
	if err != nil {
		msg := fmt.Sprintf("error reading blocklist: %v", err)
		e := newEvent(ErrorReadingBlockList, msg, "")
		d.eventSync.report(e, d.localVolume)
		klog.Errorf(msg)
		return
	}

	if len(deviceSet) == 0 {
		klog.V(3).Infof("unable to find any new disks")
	}

	// read all available disks from /dev/disk/by-id/*
	allDiskIds, err := filepath.Glob(diskByIDPath)
	if err != nil {
		msg := fmt.Sprintf("error listing disks in /dev/disk/by-id: %v", err)
		e := newEvent(ErrorListingDeviceID, msg, "")
		d.eventSync.report(e, d.localVolume)
		klog.Errorf(msg)
		return
	}

	deviceMap, err := d.findMatchingDisks(diskConfig, deviceSet, allDiskIds)
	if err != nil {
		msg := fmt.Sprintf("eror finding matching disks: %v", err)
		e := newEvent(ErrorFindingMatchingDisk, msg, "")
		d.eventSync.report(e, d.localVolume)
		klog.Errorf(msg)
		return
	}

	if len(deviceMap) == 0 {
		msg := "found empty matching device list"
		e := newEvent(ErrorFindingMatchingDisk, msg, "")
		d.eventSync.report(e, d.localVolume)
		klog.Errorf(msg)
		return
	}

	for storageClass, deviceArray := range deviceMap {
		for _, deviceNameLocation := range deviceArray {
			symLinkDirPath := path.Join(d.symlinkLocation, storageClass)
			err := os.MkdirAll(symLinkDirPath, 0755)
			if err != nil {
				msg := fmt.Sprintf("error creating symlink dir %s: %v", symLinkDirPath, err)
				e := newEvent(ErrorFindingMatchingDisk, msg, "")
				d.eventSync.report(e, d.localVolume)
				klog.Errorf(msg)
				continue
			}

			// if it is a shared directory
			if deviceNameLocation.directoryPath != "" {
				bindName := generateBindName(deviceNameLocation.directoryPath, storageClass)
				bindPath := path.Join(symLinkDirPath, bindName)
				if fileExists(bindPath) {
					klog.V(4).Infof("bind path %s already exists", bindPath)
					continue
				}

				// TODO: perform actual bind mount of directoryPath to bindPath

			}

			baseDeviceName := filepath.Base(deviceNameLocation.diskNamePath)
			symLinkPath := path.Join(symLinkDirPath, baseDeviceName)
			if fileExists(symLinkPath) {
				klog.V(4).Infof("symlink %s already exists", symLinkPath)
				continue
			}
			var symLinkErr error
			if deviceNameLocation.diskID != "" {
				klog.V(3).Infof("symlinking to %s to %s", deviceNameLocation.diskID, symLinkPath)
				symLinkErr = os.Symlink(deviceNameLocation.diskID, symLinkPath)
			} else {
				klog.V(3).Infof("symlinking to %s to %s", deviceNameLocation.diskNamePath, symLinkPath)
				symLinkErr = os.Symlink(deviceNameLocation.diskNamePath, symLinkPath)
			}

			if symLinkErr != nil {
				msg := fmt.Sprintf("error creating symlink %s: %v", symLinkPath, err)
				e := newEvent(ErrorFindingMatchingDisk, msg, deviceNameLocation.diskNamePath)
				d.eventSync.report(e, d.localVolume)
				klog.Errorf(msg)
			}

			successMsg := fmt.Sprintf("found matching disk %s", baseDeviceName)
			e := newSuccessEvent(FoundMatchingDisk, successMsg, deviceNameLocation.diskNamePath)
			d.eventSync.report(e, d.localVolume)
		}
	}

}

func (d *DiskMaker) findMatchingDisks(diskConfig *DiskConfig, deviceSet sets.String, allDiskIds []string) (map[string][]DiskLocation, error) {
	// blockDeviceMap is a map of storageclass and device locations
	blockDeviceMap := make(map[string][]DiskLocation)

	addDiskToMap := func(scName, stableDeviceID, diskName, directoryPath string) {
		deviceArray, ok := blockDeviceMap[scName]
		if !ok {
			deviceArray = []DiskLocation{}
		}
		deviceArray = append(deviceArray, DiskLocation{diskName, stableDeviceID, directoryPath})
		blockDeviceMap[scName] = deviceArray
	}
	for storageClass, disks := range diskConfig.Disks {
		// handle diskNames
		deviceNames := disks.DeviceNames().List()
		for _, diskName := range deviceNames {
			baseDeviceName := filepath.Base(diskName)
			if hasExactDisk(deviceSet, baseDeviceName) {
				matchedDeviceID, err := d.findStableDeviceID(baseDeviceName, allDiskIds)
				// This means no /dev/disk/by-id entry was created for requested device.
				if err != nil {
					klog.V(4).Infof("unable to find disk ID %s for local pool %v", diskName, err)
					addDiskToMap(storageClass, "", diskName, "")
					continue
				}
				addDiskToMap(storageClass, matchedDeviceID, diskName, "")
				continue
			}
		}

		deviceIds := disks.DeviceIDs().List()
		// handle DeviceIDs
		for _, deviceID := range deviceIds {
			matchedDeviceID, matchedDiskName, err := d.findDeviceByID(deviceID)
			if err != nil {
				msg := fmt.Sprintf("unable to add disk-id %s to local disk pool: %v", deviceID, err)
				e := newEvent(ErrorFindingMatchingDisk, msg, deviceID)
				d.eventSync.report(e, d.localVolume)
				klog.Errorf(msg)
				continue
			}
			baseDeviceName := filepath.Base(matchedDiskName)
			// We need to make sure that requested device is not already mounted.
			if hasExactDisk(deviceSet, baseDeviceName) {
				addDiskToMap(storageClass, matchedDeviceID, matchedDiskName, "")
			}
		}

		for _, directory := range disks.DirectoryPaths {
			sharedDirPath := path.Join(rootfsDir, directory)
			if fileExists(sharedDirPath) {
				isDir, err := isDir(sharedDirPath)
				if err != nil {
					msg := fmt.Sprintf("error checking shared dir %s: %v", sharedDirPath, err)
					e := newEvent(ErrorCreatingSharedDir, msg, "")
					d.eventSync.report(e, d.localVolume)
					klog.Errorf(msg)
				}
				if isDir {
					addDiskToMap(storageClass, "", "", sharedDirPath)
				}
				continue
			}

			err := os.MkdirAll(sharedDirPath, 0755)
			if err != nil {
				msg := fmt.Sprintf("error creating shared dir %s: %v", sharedDirPath, err)
				e := newEvent(ErrorCreatingSharedDir, msg, "")
				d.eventSync.report(e, d.localVolume)
				klog.Errorf(msg)
				continue
			}
			addDiskToMap(storageClass, "", "", sharedDirPath)
		}
	}
	return blockDeviceMap, nil
}

// findDeviceByID finds device ID and return (deviceID, deviceName, error)
func (d *DiskMaker) findDeviceByID(deviceID string) (string, string, error) {
	diskDevPath, err := filepath.EvalSymlinks(deviceID)
	if err != nil {
		return "", "", fmt.Errorf("unable to find device with id %s", deviceID)
	}
	return deviceID, diskDevPath, nil
}

func (d *DiskMaker) findStableDeviceID(diskName string, allDisks []string) (string, error) {
	for _, diskIDPath := range allDisks {
		diskDevPath, err := filepath.EvalSymlinks(diskIDPath)
		if err != nil {
			continue
		}
		diskDevName := filepath.Base(diskDevPath)
		if diskDevName == diskName {
			return diskIDPath, nil
		}
	}
	return "", fmt.Errorf("unable to find ID of disk %s", diskName)
}

func (d *DiskMaker) findNewDisks(content string) (sets.String, error) {
	deviceSet := sets.NewString()
	deviceLines := strings.Split(content, "\n")
	for _, deviceLine := range deviceLines {
		deviceLine := strings.TrimSpace(deviceLine)
		deviceDetails := strings.Split(deviceLine, " ")
		// We only consider devices that are not mounted.
		// TODO: We should also consider checking for device partitions, so as
		// if a device has partitions then we do not consider the device. We only
		// consider partitions.
		if len(deviceDetails) == 1 && len(deviceDetails[0]) > 0 {
			deviceSet.Insert(deviceDetails[0])
		}
	}
	return deviceSet, nil
}

func hasExactDisk(disks sets.String, device string) bool {
	for _, disk := range disks.List() {
		if disk == device {
			return true
		}
	}
	return false
}

// fileExists checks if a file exists
func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return true
}

// isBlock checks if the given path is a block device
func isBlock(fullPath string) (bool, error) {
	var st unix.Stat_t
	err := unix.Stat(fullPath, &st)
	if err != nil {
		return false, err
	}

	return (st.Mode & unix.S_IFMT) == unix.S_IFBLK, nil
}

// isDir checks if the given path is a directory
func isDir(fullPath string) (bool, error) {
	dir, err := os.Open(fullPath)
	if err != nil {
		return false, err
	}
	defer dir.Close()

	stat, err := dir.Stat()
	if err != nil {
		return false, err
	}

	return stat.IsDir(), nil
}

func generateBindName(file, class string) string {
	h := fnv.New32a()
	h.Write([]byte(file))
	h.Write([]byte(class))
	// This is the FNV-1a 32-bit hash
	return fmt.Sprintf("local-shared-%x", h.Sum32())
}
