I went **only** through LocalVolume's implementation of [processRejectedDevicesForDeviceLinks](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/pkg/diskmaker/controllers/lv/reconcile.go#L373) from https://github.com/openshift/local-storage-operator/pull/615!
No LocalVolumeSets, no validDevice processing.

I recorded everything that I found strange or confusing. Most of the remarks were merged before #615, some of them a very long time ago.

1. [`processRejectedDevicesForDeviceLinks`](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/pkg/diskmaker/controllers/lv/reconcile.go#L642) gets `rejectedDevices []internal.BlockDevice`. [`BlockDevice`](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/pkg/internal/diskutil.go#L63) is a struct that contains `OwnerName`, `OwnerNamespace`, `OwnerKind`, `OwnerUID` and `OwnerAPIVersion` fields - why not [`OwnerReference`](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/vendor/k8s.io/apimachinery/pkg/apis/meta/v1/types.go#L295-L296)? I know, it's very old code, but even the passed struct is ... strange.

2. `processRejectedDevicesForDeviceLinks` calls `HasExistingLocalVolumes`
    * [Here](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/pkg/common/symlink_utils.go#L215-L216) we store return value of `FindStalePVs` to a variable called `currentDevice`. But it's not a device, it's a list of LVDLs!

2. `processRejectedDevicesForDeviceLinks()` calls `HasExistingLocalVolumes()`, which calls `LocalVolumeDeviceLinkCache.FindStalePVs()`
    * `HasExistingLocalVolumes()` finds the best `/by-id/` symlink (using `blockDevice.GetPathByID()`, but it's not relevant) 
    * `FindStalePVs` tries to find this symlink in `l.localDeviceInfos[symlink]` directly [here](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/pkg/common/pv_link_cache.go#L225-L226).
    * if not found, it tries to find all `/by-id/` symlinks [later](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/pkg/common/pv_link_cache.go#L243), incl. the best one. Why it tries the best one twice? Why does it have a special case at the beginning?
    * In addition, the `currentDeviceInfo` is [constantly being overwritten](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/pkg/common/pv_link_cache.go#L246-L247). Why?

2. `processRejectedDevicesForDeviceLinks()` calls `HasExistingLocalVolumes()`, which calls `LocalVolumeDeviceLinkCache.FindStalePVs()`
   * `FindStalePVs()` does not find any PVs, as the name would suggest. It find LVDLs for a given device and its symlinks. And what "stale" means there? There does not seem to be anything stale in the LVDLs.

4. `processRejectedDevicesForDeviceLinks` calls `HasExistingLocalVolumes`, which calls `GetSymlinkTargetPath`
    * I think we call the path in `/mnt/local-storage/<sc>/foo` as `symlinkPath`. Why is it called `TargetPath` here?
    * And what actually **is** a symlink source and target? I think source is `/mnt/local-storage/sc/foo` and the target of that symlink is `/dev/disk/by-id/scsi-1-x`. Similarly, synmlink source `/dev/disk/by-id/scsi-1-x` has target `/dev/sda`. (see `man 2 symlink`, "target" is defined there as the destination, we can extrapolate that "source" should be the symlink name). The whole code mixes "source" and "target" very often. In the API, we have `currentLinkTarget`, which is IMO correct - that's the destination of `/mnt/local-storage/sc/foo` symlink. But then here "source" means `/dev/disk/by-id` and "target" is in `/mnt`? I need to **constantly** double check what various "source" and "target" variables actually mean. This was actually the main source of cognitive overload when going through the code.

5. `processRejectedDevicesForDeviceLinks` -> `HasExistingLocalVolumes` -> `GetSymlinkTargetPath` -> `getLVDLAndPV`
	  * Why do we have "GetSomethingAndSomethingElse"? I would prefer separate `getLVDL()` and `getPV()`. Why is it mixed together? `getLVDL()` could be used in other cases. I think @dfajmon had `getLVDLAndSomethingElse()` in some of his PRs.
  
6. `processRejectedDevicesForDeviceLinks` -> `HasExistingLocalVolumes` -> `GetSymlinkTargetPath`
	  * [checking policy](https://github.com/jsafrane/local-storage-operator/blob/3e6dd4bf8d8e123747d62c8cfbca3725b59044c5/pkg/common/pv_link_cache.go#L100-L101) in this function is very surprising here. `GetSymlinkTargetPath` name suggests it will find a target path (which is actually the symlink name / source path 🙃), not execute any policy logic.

And that's where I ended. I feel like I went through a relatively small subset of `processRejectedDevicesForDeviceLinks` code, but I feel my mind is close to be overloaded.

Another confusing thing I remember from a previous review was that term "device" was often used for a symlink to a device. But I haven't found that in `processRejectedDevicesForDeviceLinks` (yet). And I can remember wrong.
