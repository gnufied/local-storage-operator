package lv

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	localv1 "github.com/openshift/local-storage-operator/api/v1"
	"github.com/openshift/local-storage-operator/api/v1alpha1"
	"github.com/openshift/local-storage-operator/pkg/common"
	test "github.com/openshift/local-storage-operator/test/framework"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/mount"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	provCache "sigs.k8s.io/sig-storage-local-static-provisioner/pkg/cache"
	provCommon "sigs.k8s.io/sig-storage-local-static-provisioner/pkg/common"
	provDeleter "sigs.k8s.io/sig-storage-local-static-provisioner/pkg/deleter"
	provUtil "sigs.k8s.io/sig-storage-local-static-provisioner/pkg/util"
)

func TestLocalVolumeReconcilerBehavior(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "LocalVolume Reconciler Behavior Suite")
}

const (
	behaviorNamespace       = "default"
	behaviorLocalVolumeName = "local-volume"
	behaviorSCName          = "local-sc"
	behaviorNodeName        = "test-node"
)

var _ = ginkgo.Describe("LocalVolumeReconciler behavior", func() {
	var (
		ctx             context.Context
		symlinkLocation string
		localVolume     *localv1.LocalVolume
		reconciler      *LocalVolumeReconciler
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		symlinkLocation = ginkgo.GinkgoT().TempDir()
		localVolume = newBehaviorLocalVolume(behaviorNamespace, behaviorLocalVolumeName, behaviorSCName, "/dev/disk/by-id/test-disk")
		previousNodeName, hadPreviousNodeName := os.LookupEnv("MY_NODE_NAME")
		gomega.Expect(os.Setenv("MY_NODE_NAME", behaviorNodeName)).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			if hadPreviousNodeName {
				gomega.Expect(os.Setenv("MY_NODE_NAME", previousNodeName)).To(gomega.Succeed())
				return
			}
			gomega.Expect(os.Unsetenv("MY_NODE_NAME")).To(gomega.Succeed())
		})

		reconciler, _ = newBehaviorReconciler(
			symlinkLocation,
			localVolume,
			newBehaviorNode(behaviorNodeName, map[string]string{
				corev1.LabelHostname: behaviorNodeName,
				"test.local/role":    "worker",
			}),
			newBehaviorProvisionerConfigMap(behaviorNamespace, symlinkLocation),
		)
	})

	ginkgo.When("the LocalVolumeDeviceLink cache has not completed its initial sync", func() {
		ginkgo.BeforeEach(func() {
			reconciler.pvLinkCache = common.NewLocalVolumeDeviceLinkCache(reconciler.Client, nil, behaviorNodeName)
		})

		ginkgo.It("requeues quickly without touching local storage", func() {
			result, err := reconciler.Reconcile(ctx, behaviorRequest(localVolume))

			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result).To(gomega.Equal(ctrl.Result{RequeueAfter: fastRequeueTime}))
			gomega.Expect(filepath.Join(symlinkLocation, behaviorSCName)).NotTo(gomega.BeADirectory())
		})
	})

	ginkgo.When("the LocalVolume node selector does not match this node", func() {
		ginkgo.BeforeEach(func() {
			localVolume.Spec.NodeSelector = &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "test.local/role",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"infra"},
							},
						},
					},
				},
			}
			gomega.Expect(reconciler.Client.Update(ctx, localVolume)).To(gomega.Succeed())
		})

		ginkgo.It("exits without creating symlink directories or requeueing", func() {
			result, err := reconciler.Reconcile(ctx, behaviorRequest(localVolume))

			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result).To(gomega.Equal(ctrl.Result{}))
			gomega.Expect(filepath.Join(symlinkLocation, behaviorSCName)).NotTo(gomega.BeADirectory())
		})
	})

	ginkgo.Context("Provisioning new devices previously unseen", func() {
		ginkgo.When("user specifies localvolume with by-id path", func() {
			ginkgo.It("should create PV and LVDL object", func() {

			})
		})
		ginkgo.When("user specifies device already mounted", func() {
			ginkgo.PIt("should not create PV and LVDL object")
		})

		ginkgo.When("user specifies localvolume without by-id path", func() {
			ginkgo.PIt("should create PV and LVDL object")
		})
		ginkgo.When(("user specifies localvolume which is already used by another SC"), func() {
			ginkgo.PIt("should not create PV and LVDL object")
		})
	})

	ginkgo.Context("Provisioning devices which were previously seen", func() {
		ginkgo.When("path in /mnt/local-storage is valid", func() {
			ginkgo.PIt("should create PV and LVDL object")
			ginkgo.When("symlink in /mnt/local-storage points to valid but non-preferred path", func() {
				ginkgo.PIt("should update LVDL object with preferred path")
			})
		})
	})

	ginkgo.When("matching devices are available", func() {
		ginkgo.PIt("provisions PVs and LocalVolumeDeviceLinks from observable cluster and filesystem state")
	})

	ginkgo.When("the LocalVolume is being deleted", func() {
		ginkgo.PIt("cleans up owned symlinks and keeps requeueing while released PV cleanup is in progress")
	})
})

func newBehaviorLocalVolume(namespace, name, storageClassName string, devicePaths ...string) *localv1.LocalVolume {
	return &localv1.LocalVolume{
		TypeMeta: metav1.TypeMeta{
			APIVersion: localv1.GroupVersion.String(),
			Kind:       localv1.LocalVolumeKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: localv1.LocalVolumeSpec{
			StorageClassDevices: []localv1.StorageClassDevice{
				{
					StorageClassName: storageClassName,
					DevicePaths:      devicePaths,
				},
			},
		},
	}
}

func newBehaviorNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

func newBehaviorProvisionerConfigMap(namespace, symlinkLocation string) *corev1.ConfigMap {
	configMapData, err := provCommon.VolumeConfigToConfigMapData(&provCommon.ProvisionerConfiguration{
		StorageClassConfig: map[string]provCommon.MountConfig{
			behaviorSCName: {
				HostDir:  symlinkLocation,
				MountDir: "/mnt/local-storage",
			},
		},
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      common.ProvisionerConfigMapName,
		},
		Data: configMapData,
	}
}

func newBehaviorReconciler(symlinkLocation string, objects ...runtime.Object) (*LocalVolumeReconciler, *testContext) {
	scheme, err := localv1.SchemeBuilder.Build()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(localv1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(v1alpha1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(corev1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(storagev1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(appsv1.AddToScheme(scheme)).To(gomega.Succeed())

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&localv1.LocalVolumeDeviceLink{}).
		WithRuntimeObjects(objects...).
		Build()
	fakeRecorder := record.NewFakeRecorder(20)
	mounter := &mount.FakeMounter{MountPoints: []mount.MountPoint{}}
	fakeVolUtil := provUtil.NewFakeVolumeUtil(false, map[string][]*provUtil.FakeDirEntry{})
	runtimeConfig := &provCommon.RuntimeConfig{
		UserConfig: &provCommon.UserConfig{
			Node:         &corev1.Node{},
			DiscoveryMap: make(map[string]provCommon.MountConfig),
		},
		Cache:    provCache.NewVolumeCache(),
		VolUtil:  fakeVolUtil,
		APIUtil:  test.ApiUtil{Client: fakeClient},
		Recorder: fakeRecorder,
		Mounter:  mounter,
	}
	testContext := &testContext{
		fakeClient:    fakeClient,
		fakeRecorder:  fakeRecorder,
		fakeMounter:   mounter,
		runtimeConfig: runtimeConfig,
		fakeVolUtil:   fakeVolUtil,
	}
	pvLinkCache := common.NewLocalVolumeDeviceLinkCache(fakeClient, nil, behaviorNodeName)
	pvLinkCache.MarkSyncedForTests()

	reconciler := NewLocalVolumeReconciler(
		fakeClient,
		fakeClient,
		scheme,
		symlinkLocation,
		&provDeleter.CleanupStatusTracker{ProcTable: provDeleter.NewProcTable()},
		runtimeConfig,
		pvLinkCache,
	)

	return reconciler, testContext
}

func behaviorRequest(localVolume *localv1.LocalVolume) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Namespace: localVolume.Namespace,
			Name:      localVolume.Name,
		},
	}
}
