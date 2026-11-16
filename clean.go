package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type cleanItem struct {
	ID    string        `json:"id"`
	Bytes int64         `json:"bytes"`
	Files int           `json:"files"`
	Note  string        `json:"note,omitempty"` // "running", "admin"
	Dirs  []string      `json:"-"`
	Age   time.Duration `json:"-"`
}

// dirSize adds up the files under root that are older than age.
func dirSize(root string, age time.Duration, now time.Time) (bytes int64, files int) {
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil && now.Sub(fi.ModTime()) >= age {
			bytes += fi.Size()
			files++
		}
		return nil
	})
	return
}

// cleanDir removes the files under root that are older than age and the
// folders left empty. root itself stays. Files in use are skipped.
func cleanDir(root string, age time.Duration, now time.Time) (freed int64, removed int) {
	var dirs []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root {
				dirs = append(dirs, p)
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil || now.Sub(fi.ModTime()) < age {
			return nil
		}
		if os.Remove(p) == nil {
			freed += fi.Size()
			removed++
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // fails on a folder that is not empty, which is what we want
	}
	return
}

type binEntry struct {
	Info, Data string
	Size       int64
	Deleted    time.Time
}

// ParseBinInfo reads a $I file of the Windows recycle bin: the size of the
// deleted item and the moment it was deleted.
func ParseBinInfo(b []byte) (size int64, deleted time.Time, ok bool) {
	if len(b) < 24 {
		return 0, time.Time{}, false
	}
	v := binary.LittleEndian.Uint64(b[0:8])
	if v != 1 && v != 2 {
		return 0, time.Time{}, false
	}
	size = int64(binary.LittleEndian.Uint64(b[8:16]))
	ft := int64(binary.LittleEndian.Uint64(b[16:24]))
	if ft <= 0 || size < 0 {
		return 0, time.Time{}, false
	}
	// FILETIME: 100 ns ticks since 1601
	deleted = time.Unix(ft/10000000-11644473600, 0)
	return size, deleted, true
}

// binEntries lists what lies in the recycle bin folders longer than age.
func binEntries(binRoots []string, age time.Duration, now time.Time) []binEntry {
	var out []binEntry
	for _, root := range binRoots {
		sids, _ := os.ReadDir(root)
		for _, sid := range sids {
			if !sid.IsDir() {
				continue
			}
			dir := filepath.Join(root, sid.Name())
			files, _ := os.ReadDir(dir)
			for _, f := range files {
				n := f.Name()
				if !strings.HasPrefix(n, "$I") || f.IsDir() {
					continue
				}
				b, err := os.ReadFile(filepath.Join(dir, n))
				if err != nil {
					continue
				}
				size, del, ok := ParseBinInfo(b)
				if !ok || now.Sub(del) < age {
					continue
				}
				out = append(out, binEntry{Info: filepath.Join(dir, n), Data: filepath.Join(dir, "$R"+n[2:]), Size: size, Deleted: del})
			}
		}
	}
	return out
}

func cleanBin(entries []binEntry) (freed int64, removed int) {
	for _, e := range entries {
		if err := os.RemoveAll(e.Data); err != nil {
			continue
		}
		os.Remove(e.Info)
		freed += e.Size
		removed++
	}
	return
}
