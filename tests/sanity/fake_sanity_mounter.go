package sanity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	k8smount "k8s.io/utils/mount"

	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
)

type fakeSanityMount struct {
	source  string
	fstype  string
	options []string
}

type fakeSanityMounter struct {
	mu     sync.Mutex
	mounts map[string]fakeSanityMount
}

var _ k8smount.Interface = (*fakeSanityMounter)(nil)

func newFakeSanityMounter() *fakeSanityMounter {
	return &fakeSanityMounter{mounts: make(map[string]fakeSanityMount)}
}

func newFakeSanitySafeMounter(mounter *fakeSanityMounter) *mountmanager.SafeFormatAndMount {
	return &mountmanager.SafeFormatAndMount{
		SafeFormatAndMount: &k8smount.SafeFormatAndMount{Interface: mounter},
	}
}

func (m *fakeSanityMounter) Mount(source, target, fstype string, options []string) error {
	return m.mount(source, target, fstype, options)
}

func (m *fakeSanityMounter) MountSensitive(source, target, fstype string, options, sensitiveOptions []string) error {
	allOptions := make([]string, 0, len(options)+len(sensitiveOptions))
	allOptions = append(allOptions, options...)
	allOptions = append(allOptions, sensitiveOptions...)
	return m.mount(source, target, fstype, allOptions)
}

func (m *fakeSanityMounter) mount(source, target, fstype string, options []string) error {
	if target == "" {
		return fmt.Errorf("mount target is required")
	}

	target = filepath.Clean(target)
	if err := os.MkdirAll(target, 0o750); err != nil {
		return fmt.Errorf("create mount target %q: %w", target, err)
	}

	requested := fakeSanityMount{
		source:  source,
		fstype:  fstype,
		options: append([]string(nil), options...),
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, found := m.mounts[target]; found {
		if existing.source == requested.source && existing.fstype == requested.fstype && equalMountOptions(existing.options, requested.options) {
			return nil
		}
		return fmt.Errorf("mount target %q is already mounted from %q as %q with options %v; cannot mount from %q as %q with options %v", target, existing.source, existing.fstype, existing.options, requested.source, requested.fstype, requested.options)
	}

	m.mounts[target] = requested
	return nil
}

func (m *fakeSanityMounter) Unmount(target string) error {
	if target == "" {
		return fmt.Errorf("unmount target is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.mounts, filepath.Clean(target))
	return nil
}

func (m *fakeSanityMounter) List() ([]k8smount.MountPoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	paths := make([]string, 0, len(m.mounts))
	for path := range m.mounts {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	mounts := make([]k8smount.MountPoint, 0, len(paths))
	for _, path := range paths {
		mounted := m.mounts[path]
		mounts = append(mounts, k8smount.MountPoint{
			Device: mounted.source,
			Path:   path,
			Type:   mounted.fstype,
			Opts:   append([]string(nil), mounted.options...),
		})
	}
	return mounts, nil
}

func (m *fakeSanityMounter) IsLikelyNotMountPoint(path string) (bool, error) {
	if path == "" {
		return true, &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
	}

	m.mu.Lock()
	_, mounted := m.mounts[filepath.Clean(path)]
	m.mu.Unlock()
	if mounted {
		return false, nil
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return true, err
		}
		return true, fmt.Errorf("stat mount point %q: %w", path, err)
	}
	return true, nil
}

func (m *fakeSanityMounter) GetMountRefs(path string) ([]string, error) {
	cleanPath := filepath.Clean(path)

	m.mu.Lock()
	defer m.mu.Unlock()

	root := m.mountRoot(cleanPath)
	refs := make([]string, 0)
	for target := range m.mounts {
		if target != cleanPath && m.mountRoot(target) == root {
			refs = append(refs, target)
		}
	}
	sort.Strings(refs)
	return refs, nil
}

func (m *fakeSanityMounter) mountRoot(path string) string {
	visited := make(map[string]struct{})
	for {
		if _, seen := visited[path]; seen {
			return path
		}
		visited[path] = struct{}{}

		mounted, found := m.mounts[path]
		if !found {
			return path
		}
		path = mounted.source
	}
}

func equalMountOptions(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
