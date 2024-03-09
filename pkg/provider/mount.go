package provider

import (
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/containerd/mount"
	"github.com/openfaas/faasd/pkg"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

var rmlogger = log.With().
	Str("component", "[RootfsManager]").
	Logger()

// we only consider overlayfs here
type OverlayInfo struct {
	lower  string
	upper  string
	work   string
	merged string
}

type AppOverlayInfo struct {
	OverlayInfo
	serviceName string
}

type AppOverlayCache struct {
	// serviceName -> IDLE app overlays
	data []*AppOverlayInfo
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

// NOTE by huang-jl: This method should only be called once
func NewRootfsManager() (*RootfsManager, error) {
	m := &RootfsManager{
		packageBase:  pkg.FaasdPackageDirPrefix,
		appPkgs:      make(map[string]bool),
		cache:        make(map[string]*AppOverlayCache),
		appOverlayCh: make(chan string, 40),
	}

	// try umount old overlay if possible
	items, err := os.ReadDir(pkg.FaasdAppMergeDirPrefix)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read dir %s failed", pkg.FaasdAppMergeDirPrefix)
	} else if err == nil {
		for _, item := range items {
			p := path.Join(pkg.FaasdAppMergeDirPrefix, item.Name())
			unix.Unmount(p, unix.MNT_DETACH)
		}
	}
	// make a clean app dir
	for _, dir := range [3]string{pkg.FaasdAppWorkDirPrefix,
		pkg.FaasdAppUpperDirPrefix, pkg.FaasdAppMergeDirPrefix} {
		if err = os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("error when clean %s dir %s", dir, err)
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("mkdir %s failed", dir)
		}
	}
	if _, err := os.Stat(m.packageBase); err != nil {
		if os.IsNotExist(err) {
			if err = os.MkdirAll(m.packageBase, 0755); err != nil {
				return nil, fmt.Errorf("mkdir %s failed %s", m.packageBase, err)
			}
		} else {
			return nil, fmt.Errorf("stat %s failed %s", m.packageBase, err)
		}
	}

	// init the rootfsManager.appPkgs and appOverlay cache
	appPkgs, err := os.ReadDir(m.packageBase)
	if err != nil {
		return nil, fmt.Errorf("Read dirs of %s failed: %s", m.packageBase, err)
	}
	for _, appPkg := range appPkgs {
		m.appPkgs[appPkg.Name()] = true
		rmlogger.Info().Str("pkg", appPkg.Name()).Msg("Init app pkgs")
	}

	// TODO(huang-jl) This is a simple cache fill strategy
	// use one will fill two more until reach an upper bound
	go func() {
		for serviceName := range m.appOverlayCh {
			m.mu.Lock()
			cache, exist := m.cache[serviceName]
			if !exist {
				rmlogger.Error().Str("service name", serviceName).Msg("cannot find service in cache")
				m.mu.Unlock()
				continue
			}
			if len(cache.data) >= pkg.AppOverlayCacheLimit {
				m.mu.Unlock()
				continue
			}
			m.mu.Unlock()

			if err := m.fillAppOverlayCache(serviceName, 2); err != nil {
				rmlogger.Error().Err(err).Str("service name", serviceName).Msg("fill app overlay cache failed")
			}
		}
	}()
	return m, nil
}

// Need initialize app overlay cache for each service
func (m *RootfsManager) RegisterService(serviceName string) error {
	m.mu.Lock()
	_, exist := m.cache[serviceName]
	if !exist {
		m.cache[serviceName] = &AppOverlayCache{}
		m.mu.Unlock()
		if err := m.fillAppOverlayCache(serviceName, pkg.AppOverlayCacheInitNum); err != nil {
			return fmt.Errorf("fill app overlay for %s failed", serviceName)
		}
	} else {
		// if already exist, then we do nothing
		m.mu.Unlock()
	}
	return nil
}

// TODO(huang-jl) clear app overlay's writable layer
func (m *RootfsManager) putAppOverlayToCache(info *AppOverlayInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[info.serviceName].data = append(m.cache[info.serviceName].data, info)
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

// Returned *OverlayInfo is nil which means there is no entry in cache
func (m *RootfsManager) getAppOverlayFromCache(serviceName string) (*AppOverlayInfo, error) {
	var res *AppOverlayInfo
	m.mu.Lock()
	defer m.mu.Unlock()
	appOverlays, exist := m.cache[serviceName]
	if !exist {
		return res, fmt.Errorf("%s not exists in app overlay cache", serviceName)
	}
	if len(appOverlays.data) == 0 {
		return res, nil
	}
	// pop the element in the front
	res = appOverlays.data[0]
	appOverlays.data = appOverlays.data[1:]
	return res, nil
}

// umount app overlay and put it into app overlay cache
func (m *RootfsManager) recyleAppOverlay(ctr *ContainerdCtr) {
	if ctr.appOverlay == nil {
		return
	}
	targetBindPath := path.Join(ctr.rootfs.merged, "home/app")
	unix.Unmount(targetBindPath, unix.MNT_DETACH) // [0.2ms]
	m.putAppOverlayToCache(ctr.appOverlay)
	ctr.appOverlay = nil
}

// I choose one goroutine to do fill overlaycache job
func (m *RootfsManager) fillAppOverlayCache(serviceName string, num int) error {
	appOverlays := make([]*AppOverlayInfo, num)
	// Do not hold lock while prepare overlay
	for i := 0; i < num; i++ {
		appOverlay, err := m.PrepareAppOverlay(serviceName, false)
		if err != nil {
			return err
		}
		appOverlays[i] = appOverlay
	}

	m.mu.Lock()
	cache := m.cache[serviceName] // must exist
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
	lambdaName := ServiceName2LambdaName(serviceName)
	val, exist := m.appPkgs[lambdaName]
	if val && exist {
		return path.Join(m.packageBase, lambdaName), nil
	}
	return "", ErrPkgDirNotFind
}

// Prepare the app overlay, including mkdir for upperdir and workdir
//
// Return the mounted path of app dir overlay
func (m *RootfsManager) PrepareAppOverlay(serviceName string, showLog bool) (*AppOverlayInfo, error) {
	var res *AppOverlayInfo
	start := time.Now()
	pkgPath, err := m.lookupPkg(serviceName)
	if err != nil {
		return res, errors.Wrapf(err, "lookup service app package %s failed", serviceName)
	}
	if showLog {
		rmlogger.Debug().Str("service name", serviceName).Dur("lookup pkg", time.Since(start)).Send()
	}
	// NOTE by huang-jl: appOverlayID has no relationship with instanceID
	appOverlayID := m.allocateAppOverlayID(serviceName)
	start = time.Now()
	name := GetInstanceID(serviceName, appOverlayID)

	upperdir := path.Join(pkg.FaasdAppUpperDirPrefix, name)
	workdir := path.Join(pkg.FaasdAppWorkDirPrefix, name)
	mergedir := path.Join(pkg.FaasdAppMergeDirPrefix, name)
	for _, dir := range []string{upperdir, workdir, mergedir} {
		if err := os.Mkdir(dir, 0755); err != nil {
			return res, errors.Wrapf(err, "make dir %s", dir)
		}
	}
	if showLog {
		rmlogger.Debug().Str("service name", serviceName).Dur("make dirs", time.Since(start)).Send()
	}

	start = time.Now()
	opts := fmt.Sprintf("index=off,lowerdir=%s,upperdir=%s,workdir=%s", pkgPath, upperdir, workdir)
	if err = unix.Mount("overlay", mergedir, "overlay", unix.MS_MGC_VAL, opts); err != nil {
		return res, errors.Wrapf(err, "mount overlay to %s failed", mergedir)
	}
	if showLog {
		rmlogger.Debug().Str("service name", serviceName).Dur("mount app overlay", time.Since(start)).Send()
	}
	// return the app overlay
	res = &AppOverlayInfo{
		OverlayInfo{
			upper:  upperdir,
			work:   workdir,
			merged: mergedir,
			lower:  pkgPath,
		}, serviceName,
	}
	return res, nil
}

// When switching rootfs of an existing (or old) container:
//  1. umount the old bind mount
//  2. umount the old overlay async
//  3. mount a new app's overlay (in mose cases from a app overlay pool)
//  4. bind mount this new overlay into the rootfs of old container
//
// The reason:
//  1. Why choosing another bind mount here instead of mount overlay directly:
//     The time spent on umount in critical path.
//  2. Why we use overlay instead of bind mount lowerdir directly:
//     We need read-write capability of the dir.
//
// NOTE by huang-jl add overlayfs pool:
// I find this method sometimes can cause about 10ms overhead.
// However, even using overlay pool, a single bind mount sometimes can
// spend about 10ms in qemu.
func (m *RootfsManager) PrepareSwitchRootfs(serviceName string, oldCtr *ContainerdCtr) (*AppOverlayInfo, error) {
	start := time.Now()
	targetBindPath := path.Join(oldCtr.rootfs.merged, "home/app")
	m.recyleAppOverlay(oldCtr)
	rmlogger.Debug().Str("service name", serviceName).Dur("unmount old app overlay", time.Since(start)).Send()

	start = time.Now()
	appOverlay, err := m.getAppOverlayFromCache(serviceName)
	if err != nil {
		return nil, errors.Wrapf(err, "get app overlay from cache for %s failed", serviceName)
	}
	if appOverlay == nil {
		rmlogger.Debug().Str("service name", serviceName).Msg("prepare switch rootfs sync!")
		appOverlay, err = m.PrepareAppOverlay(serviceName, true)
		if err != nil {
			return nil, errors.Wrapf(err, "prepare app overlay for %s failed", serviceName)
		}
	}
	// best effort cache fill policy: non-blocking sending fill request without retry
	select {
	case m.appOverlayCh <- serviceName:
	default:
	}
	rmlogger.Debug().Str("service name", serviceName).Dur("prepare app overlay", time.Since(start)).Send()
	start = time.Now()
	err = unix.Mount(appOverlay.merged, targetBindPath, "", unix.MS_BIND, "") // [0.8ms]
	if err != nil {
		return nil, errors.Wrapf(err, "bind mount overlay %s to %s failed",
			appOverlay, targetBindPath)
	}
	rmlogger.Debug().Str("service name", serviceName).Dur("bind mount app overlay", time.Since(start)).Send()
	return appOverlay, nil
}

// parse rootfs info from snapshotter (e.g., Mounts() or View() or Prepare())
// NOTE by huang-jl: do not parse lowerdir here since it is useless and too large
func parseRootFromSnapshotter(ns string, containerID string, mounts []mount.Mount) (OverlayInfo, error) {
	var res OverlayInfo
	if len(mounts) != 1 || mounts[0].Source != "overlay" || mounts[0].Type != "overlay" {
		return res, fmt.Errorf("weird mounts from snapshotter %v !", mounts)
	}
	m := mounts[0]
	for _, opt := range m.Options {
		if upperdir, ok := strings.CutPrefix(opt, "upperdir="); ok {
			res.upper = upperdir
		} else if workdir, ok := strings.CutPrefix(opt, "workdir="); ok {
			res.work = workdir
		}
	}
	res.merged = fmt.Sprintf("/run/containerd/io.containerd.runtime.v2.task/%s/%s/rootfs",
		ns, containerID)
	return res, nil
}
