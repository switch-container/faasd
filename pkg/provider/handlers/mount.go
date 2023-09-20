package handlers

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/containerd/containerd/mount"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/openfaas/faasd/pkg"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

var ErrPkgDirNotFind = errors.New("could not find app package directory")

// we only consider overlayfs here
type RootOverlay struct {
	lowerdirs string
	upperdir  string
	workdir   string
	mergedir  string
}

// MountManager is used for mount rootfs for new container
type RootfsManager struct {
	packageBase string
}

var rootfsManager RootfsManager

// we do not use init() directly here
// since `faasd collect` will also call init()
func InitMountModule() {
	rootfsManager.packageBase = pkg.FaasdPackageDirPrefix
	_, err := os.Stat(rootfsManager.packageBase)
	if err != nil {
		if os.IsNotExist(err) {
			if err = os.MkdirAll(rootfsManager.packageBase, 0755); err != nil {
				panic(fmt.Sprintf("mkdir %s failed", rootfsManager.packageBase))
			}
		}
	}

	// try umount overlay if possible
	items, err := os.ReadDir(pkg.FaasdAppMergeDirPrefix)
	if err != nil && !os.IsNotExist(err) {
		panic(fmt.Sprintf("read dir %s failed", pkg.FaasdAppMergeDirPrefix))
	}
	for _, item := range items {
		p := path.Join(pkg.FaasdAppMergeDirPrefix, item.Name())
		unix.Unmount(p, unix.MNT_DETACH)
	}
	// clean app dir
	for _, dir := range [3]string{pkg.FaasdAppWorkDirPrefix,
		pkg.FaasdAppUpperDirPrefix, pkg.FaasdAppMergeDirPrefix} {
		if err = os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			panic(fmt.Sprintf("error when clean %s dir %s", dir, err))
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			panic(fmt.Sprintf("mkdir %s failed", dir))
		}
	}
}

// TODO(huang-jl) use in-mem cache instead of access fs everytime
func (m *RootfsManager) lookupPkg(serviceName string) (string, error) {
	p := path.Join(m.packageBase, serviceName)
	stat, err := os.Stat(p)
	if os.IsNotExist(err) || !stat.IsDir() {
		return p, ErrPkgDirNotFind
	} else if err != nil {
		return p, err
	}

	return p, nil
}

// Prepare the app overlay, including mkdir for upperdir and workdir
//
// Return the mounted path of app dir overlay
func (m *RootfsManager) PrepareAppOverlay(serviceName string, id uint64) (string, error) {
	pkgPath, err := m.lookupPkg(serviceName)
	if err != nil {
		return "", errors.Wrapf(err, "lookup service app package %s failed", serviceName)
	}
	name := GetInstanceID(serviceName, id)
	// make dir
	upperdir := path.Join(pkg.FaasdAppUpperDirPrefix, name)
	workdir := path.Join(pkg.FaasdAppWorkDirPrefix, name)
	mergedir := path.Join(pkg.FaasdAppMergeDirPrefix, name)

	for _, dir := range []string{upperdir, workdir, mergedir} {
		if err := os.Mkdir(dir, 0755); err != nil {
			return "", errors.Wrapf(err, "make dir %s", dir)
		}
	}

	opts := fmt.Sprintf("index=off,lowerdir=%s,upperdir=%s,workdir=%s", pkgPath, upperdir, workdir)
	if err = unix.Mount("overlay", mergedir, "overlay", unix.MS_MGC_VAL, opts); err != nil {
		return "", errors.Wrapf(err, "mount overlay to %s failed", mergedir)
	}
	return mergedir, nil
}

// When switching rootfs of an existing (or old) container:
//  1. umount the old bind mount
//  2. umount the old overlay async
//  3. mount a new app's overlay
//  4. bind mount this new overlay into the rootfs of old container
//
// The reason:
//  1. Why choosing another bind mount here instead of mount overlay directly:
//     The time spent on umount in critical path.
//  2. Why we use overlay instead of bind mount lowerdir directly:
//     We need read-write capability of the dir.
func (m *RootfsManager) PrepareSwitchRootfs(serviceName string, id uint64, oldInfo ContainerInfo) error {
	// clean old container's upper directory
	// start := time.Now()
	items, err := os.ReadDir(oldInfo.rootfs.upper)
	if err != nil {
		return errors.Wrapf(err, "read dir %s failed", oldInfo.rootfs.upper)
	}
	for _, item := range items {
		p := path.Join(oldInfo.rootfs.upper, item.Name())
		if err := os.RemoveAll(p); err != nil {
			return errors.Wrapf(err, "clean old upper dir %s failed", p)
		}
	}
	if err := unix.Mount("", oldInfo.rootfs.merged, "", unix.MS_REMOUNT, ""); err != nil {
		return errors.Wrapf(err, "remount merge dir %s failed", oldInfo.rootfs.merged)
	}
	// log.Printf("clean old container's writable layer spent %s\n", time.Since(start))

	// start = time.Now()
	targetBindPath := path.Join(oldInfo.rootfs.merged, "home/app")
	unix.Unmount(targetBindPath, unix.MNT_DETACH) // [0.2ms]
	go func() {
		oldInstanceID := GetInstanceID(oldInfo.serviceName, oldInfo.id)
		// umount old app overlay fs
		oldOverlay := path.Join(pkg.FaasdAppMergeDirPrefix, oldInstanceID)
		unix.Unmount(oldOverlay, unix.MNT_DETACH)
		// remove upper and work dir of old app overlay
		os.RemoveAll(path.Join(pkg.FaasdAppWorkDirPrefix, oldInstanceID))
		os.RemoveAll(path.Join(pkg.FaasdAppUpperDirPrefix, oldInstanceID))
	}()
	// log.Printf("unmount old app dir spent %s\n", time.Since(start))

	// start = time.Now()
	appOverlay, err := m.PrepareAppOverlay(serviceName, id)
	if err != nil {
		return errors.Wrapf(err, "prepare app overlay for %s-%d failed", serviceName, id)
	}
	err = unix.Mount(appOverlay, targetBindPath, "", unix.MS_BIND, "") // [0.8ms]
	if err != nil {
		return errors.Wrapf(err, "bind mount overlay %s to %s failed",
			appOverlay, targetBindPath)
	}
	// log.Printf("mount new overlay dir spent %s\n", time.Since(start))

	return nil
}

// parse rootfs info from snapshotter (e.g., Mounts() or View() or Prepare())
func parseRootFromSnapshotter(ns string, containerID string, mounts []mount.Mount) (RootOverlay, error) {
	var res RootOverlay
	if len(mounts) != 1 || mounts[0].Source != "overlay" || mounts[0].Type != "overlay" {
		return res, fmt.Errorf("weird mounts from snapshotter %v !", mounts)
	}
	m := mounts[0]
	for _, opt := range m.Options {
		if upperdir, ok := strings.CutPrefix(opt, "upperdir="); ok {
			res.upperdir = upperdir
		}
		if workdir, ok := strings.CutPrefix(opt, "workdir="); ok {
			res.workdir = workdir
		}
		if lowerdirs, ok := strings.CutPrefix(opt, "lowerdir="); ok {
			res.lowerdirs = lowerdirs
		}
	}
	res.mergedir = fmt.Sprintf("/run/containerd/io.containerd.runtime.v2.task/%s/%s/rootfs",
		ns, containerID)
	return res, nil
}

// getOSMounts provides a mount for os-specific files such
// as the hosts file and resolv.conf
func getOSMounts() []specs.Mount {
	// Prior to hosts_dir env-var, this value was set to
	// os.Getwd()
	hostsDir := "/var/lib/faasd"
	mounts := []specs.Mount{}
	mounts = append(mounts, specs.Mount{
		Destination: "/etc/resolv.conf",
		Type:        "bind",
		Source:      path.Join(hostsDir, "resolv.conf"),
		Options:     []string{"rbind", "ro"},
	})

	mounts = append(mounts, specs.Mount{
		Destination: "/etc/hosts",
		Type:        "bind",
		Source:      path.Join(hostsDir, "hosts"),
		Options:     []string{"rbind", "ro"},
	})
	return mounts
}
