package handlers

import (
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/containerd/mount"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/openfaas/faasd/pkg"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

var ErrPkgDirNotFind = errors.New("could not find app package directory")
var ErrAppOverlayNotFind = errors.New("could not find idle app overlay")

// we only consider overlayfs here
type OverlayInfo struct {
	lower  string
	upper  string
	work   string
	merged string
}

type AppOverlayCache struct {
	// serviceName -> IDLE app overlays
	data []*OverlayInfo
	id   atomic.Uint64
}

// MountManager is used for mount rootfs for new container
type RootfsManager struct {
	packageBase string          // default: /var/lib/faasd/pkgs
	appPkgs     map[string]bool // read-only maps, do not needs mutex
	cache       map[string]*AppOverlayCache

	// TODO(huang-jl) use mutex per app instead a big lock
	mu           sync.Mutex // protect app overlay cache
	appOverlayCh chan string
}

var rootfsManager RootfsManager

// we do not use init() directly here
// since `faasd collect` will also call init()
func InitMountModule() {
	rootfsManager.packageBase = pkg.FaasdPackageDirPrefix
	rootfsManager.appPkgs = make(map[string]bool)
	rootfsManager.cache = make(map[string]*AppOverlayCache)
	rootfsManager.appOverlayCh = make(chan string, 40)

	// try umount old overlay if possible
	items, err := os.ReadDir(pkg.FaasdAppMergeDirPrefix)
	if err != nil && !os.IsNotExist(err) {
		panic(fmt.Sprintf("read dir %s failed", pkg.FaasdAppMergeDirPrefix))
	}
	for _, item := range items {
		p := path.Join(pkg.FaasdAppMergeDirPrefix, item.Name())
		unix.Unmount(p, unix.MNT_DETACH)
	}
	// make a clean app dir
	for _, dir := range [3]string{pkg.FaasdAppWorkDirPrefix,
		pkg.FaasdAppUpperDirPrefix, pkg.FaasdAppMergeDirPrefix} {
		if err = os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			panic(fmt.Sprintf("error when clean %s dir %s", dir, err))
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			panic(fmt.Sprintf("mkdir %s failed", dir))
		}
	}
	if _, err := os.Stat(rootfsManager.packageBase); err != nil {
		if os.IsNotExist(err) {
			if err = os.MkdirAll(rootfsManager.packageBase, 0755); err != nil {
				panic(fmt.Sprintf("mkdir %s failed %s", rootfsManager.packageBase, err))
			}
		} else {
			panic(fmt.Sprintf("stat %s failed %s", rootfsManager.packageBase, err))
		}
	}

	// init the rootfsManager.appPkgs and appOverlay cache
	appPkgs, err := os.ReadDir(rootfsManager.packageBase)
	if err != nil {
		panic(fmt.Sprintf("Read dirs of %s failed: %s", rootfsManager.packageBase, err))
	}
	for _, appPkg := range appPkgs {
		rootfsManager.appPkgs[appPkg.Name()] = true
		rootfsManager.cache[appPkg.Name()] = &AppOverlayCache{}
		if err := rootfsManager.fillAppOverlayCache(appPkg.Name(), pkg.AppOverlayCacheInitNum); err != nil {
			panic(fmt.Sprintf("fill app overlay for %s failed", appPkg.Name()))
		}
		log.Printf("Init app pkgs %s\n", appPkg.Name())
	}

	// TODO(huang-jl) This is a simple cache fill strategy
	// use one will fill two more until reach an upper bound
	go func() {
		for serviceName := range rootfsManager.appOverlayCh {
			rootfsManager.mu.Lock()
			cache, exist := rootfsManager.cache[serviceName]
			if !exist {
				log.Printf("Err: cannot find %s in cache\n", serviceName)
				rootfsManager.mu.Unlock()
				continue
			}
			if len(cache.data) >= pkg.AppOverlayCacheLimit {
				rootfsManager.mu.Unlock()
				continue
			}
			rootfsManager.mu.Unlock()

			if err := rootfsManager.fillAppOverlayCache(serviceName, 2); err != nil {
				log.Printf("[RootfsManager] fill app overlay cache for %s failed: %s\n", serviceName, err)
			}
		}
	}()
}

// TODO(huang-jl) clear app overlay's writable layer
func (m *RootfsManager) putAppOverlayToCache(serviceName string, info *OverlayInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[serviceName].data = append(m.cache[serviceName].data, info)
	// start := time.Now()
	// items, err := os.ReadDir(oldInfo.rootfs.upper)
	// if err != nil {
	// 	return errors.Wrapf(err, "read dir %s failed", oldInfo.rootfs.upper)
	// }
	// for _, item := range items {
	// 	p := path.Join(oldInfo.rootfs.upper, item.Name())
	// 	if err := os.RemoveAll(p); err != nil {
	// 		return errors.Wrapf(err, "clean old upper dir %s failed", p)
	// 	}
	// }
	// if err := unix.Mount("", oldInfo.rootfs.merged, "", unix.MS_REMOUNT, ""); err != nil {
	// 	return errors.Wrapf(err, "remount merge dir %s failed", oldInfo.rootfs.merged)
	// }
	// log.Printf("clean old container's writable layer spent %s\n", time.Since(start))
}

func (m *RootfsManager) getAppOverlayFromCache(serviceName string) (*OverlayInfo, error) {
	var res *OverlayInfo
	m.mu.Lock()
	defer m.mu.Unlock()
	appOverlays, exist := m.cache[serviceName]
	if !exist {
		return res, fmt.Errorf("%s not exists in app overlay cache", serviceName)
	}
	if len(appOverlays.data) == 0 {
		return res, ErrAppOverlayNotFind
	}
	// pop the element in the front
	res = appOverlays.data[0]
	appOverlays.data = appOverlays.data[1:]
	return res, nil
}

// I choose one goroutine to do fill overlaycache job
func (m *RootfsManager) fillAppOverlayCache(serviceName string, num int) error {
	appOverlays := make([]*OverlayInfo, num)
	cache := m.cache[serviceName] // must exist

	// Do not hold lock while prepare overlay
	for i := 0; i < num; i++ {
		appOverlay, err := m.PrepareAppOverlay(serviceName, false)
		if err != nil {
			return err
		}
		appOverlays[i] = appOverlay
	}

	m.mu.Lock()
	cache.data = append(cache.data, appOverlays...)
	m.mu.Unlock()
	return nil
}

func (m *RootfsManager) allocateAppOverlayID(serviceName string) uint64 {
	// must exist or throw errors
	cache := m.cache[serviceName]
	return cache.id.Add(1)
}

func (m *RootfsManager) lookupPkg(serviceName string) (string, error) {
	val, exist := m.appPkgs[serviceName]
	if val && exist {
		return path.Join(m.packageBase, serviceName), nil
	}
	return "", ErrPkgDirNotFind
}

// Prepare the app overlay, including mkdir for upperdir and workdir
//
// Return the mounted path of app dir overlay
func (m *RootfsManager) PrepareAppOverlay(serviceName string, showLog bool) (*OverlayInfo, error) {
	var res *OverlayInfo
	start := time.Now()
	pkgPath, err := m.lookupPkg(serviceName)
	if err != nil {
		return res, errors.Wrapf(err, "lookup service app package %s failed", serviceName)
	}
	if showLog {
		log.Printf("lookup pkg spent %s\n", time.Since(start))
	}
	// NOTE by huang-jl: appOverlayID has no relationship with instanceID
	appOverlayID := m.allocateAppOverlayID(serviceName)
	start = time.Now()
	name := GetInstanceID(serviceName, appOverlayID)
	// make dir
	upperdir := path.Join(pkg.FaasdAppUpperDirPrefix, name)
	workdir := path.Join(pkg.FaasdAppWorkDirPrefix, name)
	mergedir := path.Join(pkg.FaasdAppMergeDirPrefix, name)
	for _, dir := range []string{upperdir, workdir, mergedir} {
		if err := os.Mkdir(dir, 0755); err != nil {
			return res, errors.Wrapf(err, "make dir %s", dir)
		}
	}
	if showLog {
		log.Printf("make dirs for overlay spent %s\n", time.Since(start))
	}

	start = time.Now()
	opts := fmt.Sprintf("index=off,lowerdir=%s,upperdir=%s,workdir=%s", pkgPath, upperdir, workdir)
	if err = unix.Mount("overlay", mergedir, "overlay", unix.MS_MGC_VAL, opts); err != nil {
		return res, errors.Wrapf(err, "mount overlay to %s failed", mergedir)
	}
	if showLog {
		log.Printf("mount app overlay spent %s\n", time.Since(start))
	}
	// return the app overlay
	res = &OverlayInfo{
		upper:  upperdir,
		work:   workdir,
		merged: mergedir,
		lower:  pkgPath,
	}
	return res, nil
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
//
// Note by huang-jl add overlayfs pool:
// I find this method sometimes can cause about 10ms overhead.
// However, even using overlay pool, a single bind mount sometimes can
// spend about 10ms
func (m *RootfsManager) PrepareSwitchRootfs(serviceName string, oldInfo ContainerInfo) (*OverlayInfo, error) {
	start := time.Now()
	targetBindPath := path.Join(oldInfo.rootfs.merged, "home/app")
	if oldInfo.appOverlay != nil {
		unix.Unmount(targetBindPath, unix.MNT_DETACH) // [0.2ms]
		m.putAppOverlayToCache(oldInfo.serviceName, oldInfo.appOverlay)
		oldInfo.appOverlay = nil
	}
	log.Printf("unmount old app dir spent %s\n", time.Since(start))

	start = time.Now()
	appOverlay, err := m.getAppOverlayFromCache(serviceName)
	if err != nil {
		if errors.Is(err, ErrAppOverlayNotFind) {
			appOverlay, err = m.PrepareAppOverlay(serviceName, true)
			if err != nil {
				return nil, errors.Wrapf(err, "prepare app overlay for %s failed", serviceName)
			}
		} else {
			return nil, errors.Wrapf(err, "get app overlay from cache for %s failed", serviceName)
		}
	}
	// TODO(huang-jl) maybe stuck here...
	m.appOverlayCh <- serviceName
	log.Printf("prepare app overlay for %s spent %s\n", serviceName, time.Since(start))
	start = time.Now()
	err = unix.Mount(appOverlay.merged, targetBindPath, "", unix.MS_BIND, "") // [0.8ms]
	if err != nil {
		return nil, errors.Wrapf(err, "bind mount overlay %s to %s failed",
			appOverlay, targetBindPath)
	}
	log.Printf("bind mount app overlay dir to rootfs spent %s\n", time.Since(start))

	return appOverlay, nil
}

// parse rootfs info from snapshotter (e.g., Mounts() or View() or Prepare())
func parseRootFromSnapshotter(ns string, containerID string, mounts []mount.Mount) (OverlayInfo, error) {
	var res OverlayInfo
	if len(mounts) != 1 || mounts[0].Source != "overlay" || mounts[0].Type != "overlay" {
		return res, fmt.Errorf("weird mounts from snapshotter %v !", mounts)
	}
	m := mounts[0]
	for _, opt := range m.Options {
		if upperdir, ok := strings.CutPrefix(opt, "upperdir="); ok {
			res.upper = upperdir
		}
		if workdir, ok := strings.CutPrefix(opt, "workdir="); ok {
			res.work = workdir
		}
		if lowerdirs, ok := strings.CutPrefix(opt, "lowerdir="); ok {
			res.lower = lowerdirs
		}
	}
	res.merged = fmt.Sprintf("/run/containerd/io.containerd.runtime.v2.task/%s/%s/rootfs",
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
