package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// main() clears the umask so create/mkdir modes apply exactly; tests
	// exercising those paths need the same environment.
	syscall.Umask(0)
	os.Exit(m.Run())
}

const (
	testMinAge = int64(1600000000) // 2020-09-13
	testMaxAge = int64(1700000000) // 2023-11-14
)

// setTestConfig points the global cfg at a temp dir with a small, fast
// shape. Tests mutate cfg further as needed, then must call
// initMetadataFastPath() if they change UIDs/GIDs or the age range.
func setTestConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg = Config{
		BaseDir:          filepath.Join(dir, "base"),
		LogFile:          filepath.Join(dir, "files.log"),
		TargetDirs:       40,
		TargetFiles:      500,
		MinFileAge:       testMinAge,
		MaxFileAge:       testMaxAge,
		UIDs:             []int{os.Getuid()},
		GIDs:             []int{os.Getgid()},
		Workers:          8,
		MaxDepth:         4,
		MaxSubdirsPerDir: 3,
		FileExtensions: map[string][]string{
			"home":    {".txt", ".md"},
			"scratch": {".log", ".tmp"},
		},
	}
	cfg.FileSize.MinNormal = 0
	cfg.FileSize.MaxNormal = 512
	cfg.FileSize.LargeFileChance = 0
	initMetadataFastPath()
}

// walkTree returns all regular files and all directories (excluding root
// itself) under root.
func walkTree(t *testing.T, root string) (files, dirs []string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		} else if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files, dirs
}

func countLogLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if len(sc.Text()) > 0 {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan log: %v", err)
	}
	return n
}

func modeSet(modes []os.FileMode) map[os.FileMode]bool {
	set := make(map[os.FileMode]bool, len(modes))
	for _, m := range modes {
		set[m] = true
	}
	return set
}

// checkTimes asserts atime and mtime are inside the configured range.
func checkTimes(t *testing.T, path string) {
	t.Helper()
	checkMtime(t, path)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if atime := st.Atim.Sec; atime < testMinAge || atime > testMaxAge {
		t.Errorf("%s: atime %d outside [%d, %d]", path, atime, testMinAge, testMaxAge)
	}
}

// checkMtime asserts only mtime. Used for directories: merely listing a
// directory (as the test's own WalkDir does) bumps its atime under
// relatime, so a dir atime assertion would test the walk, not the code.
func checkMtime(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if mtime := info.ModTime().Unix(); mtime < testMinAge || mtime > testMaxAge {
		t.Errorf("%s: mtime %d outside [%d, %d]", path, mtime, testMinAge, testMaxAge)
	}
}

func TestComputeFileCountsExactTotal(t *testing.T) {
	cases := []struct{ nDirs, target int }{
		{1, 0}, {1, 10}, {10, 10}, {10, 3}, {3, 1}, {50, 49},
		{100, 1000}, {1000, 10}, {7, 200000}, {123, 4567},
	}
	for _, tc := range cases {
		// Jitter is random; hammer each shape to catch drift.
		for iter := 0; iter < 25; iter++ {
			counts := computeFileCounts(tc.nDirs, tc.target)
			if len(counts) != tc.nDirs {
				t.Fatalf("nDirs=%d target=%d: got %d counts", tc.nDirs, tc.target, len(counts))
			}
			total := 0
			for i, n := range counts {
				if n < 0 {
					t.Fatalf("nDirs=%d target=%d: counts[%d] = %d < 0", tc.nDirs, tc.target, i, n)
				}
				total += n
			}
			if total != tc.target {
				t.Fatalf("nDirs=%d target=%d: total %d, files would be dropped or overshot", tc.nDirs, tc.target, total)
			}
		}
	}
}

func TestFastRandomFillWraps(t *testing.T) {
	buf := make([]byte, 16)
	for i := range buf {
		buf[i] = byte(i + 1)
	}
	fr := &FastRandom{buffer: buf}

	dst := make([]byte, 37)
	fr.Fill(dst)
	for k := range dst {
		if want := buf[k%16]; dst[k] != want {
			t.Fatalf("dst[%d] = %d, want %d", k, dst[k], want)
		}
	}

	// A second fill must continue from the rolling position (37 % 16 = 5).
	dst2 := make([]byte, 8)
	fr.Fill(dst2)
	for k := range dst2 {
		if want := buf[(5+k)%16]; dst2[k] != want {
			t.Fatalf("second fill: dst2[%d] = %d, want %d", k, dst2[k], want)
		}
	}
}

func TestCreateModeFor(t *testing.T) {
	cases := []struct {
		mode       os.FileMode
		wantCreate os.FileMode
		wantChmod  bool
	}{
		{0755, 0755, false},
		{0644, 0644, false},
		{0770, 0770, false},
		{0666, 0666, false},
		{0400, 0600, true},
		{0555, 0600, true},
	}
	for _, tc := range cases {
		gotCreate, gotChmod := createModeFor(tc.mode)
		if gotCreate != tc.wantCreate || gotChmod != tc.wantChmod {
			t.Errorf("createModeFor(%o) = (%o, %v), want (%o, %v)",
				tc.mode, gotCreate, gotChmod, tc.wantCreate, tc.wantChmod)
		}
	}
}

func TestGetRandomTime(t *testing.T) {
	setTestConfig(t)

	for i := 0; i < 1000; i++ {
		ts := getRandomTime().Unix()
		if ts < testMinAge || ts >= testMaxAge {
			t.Fatalf("random time %d outside [%d, %d)", ts, testMinAge, testMaxAge)
		}
	}

	// min == max means a fixed timestamp, not "now".
	cfg.MinFileAge = 1234567890
	cfg.MaxFileAge = 1234567890
	initMetadataFastPath()
	if got := getRandomTime().Unix(); got != 1234567890 {
		t.Fatalf("fixed-age time = %d, want 1234567890", got)
	}
}

func TestEstimateDirCount(t *testing.T) {
	avg, max := estimateDirCount(4, 4, 10, 200000)
	if avg > max {
		t.Errorf("avg %d > max %d", avg, max)
	}
	if avg > 200000 || max > 200000 {
		t.Errorf("estimates not clamped to target: avg=%d max=%d", avg, max)
	}
	// Depth 1 means no growth below the top level.
	avg, max = estimateDirCount(4, 4, 1, 100)
	if avg != 4 || max != 4 {
		t.Errorf("depth-1 estimates = (%d, %d), want (4, 4)", avg, max)
	}
	// Target below top-level count clamps.
	avg, max = estimateDirCount(4, 4, 3, 2)
	if avg != 2 || max != 2 {
		t.Errorf("clamped estimates = (%d, %d), want (2, 2)", avg, max)
	}
}

func TestPopulateEndToEnd(t *testing.T) {
	setTestConfig(t)

	if err := populateFilesystem(false); err != nil {
		t.Fatalf("populateFilesystem: %v", err)
	}

	files, dirs := walkTree(t, cfg.BaseDir)

	// Exactly target_files files: nothing dropped, nothing extra.
	if len(files) != cfg.TargetFiles {
		t.Errorf("files on disk = %d, want exactly %d", len(files), cfg.TargetFiles)
	}
	// Every created file must be in the log (update mode depends on it).
	if n := countLogLines(t, cfg.LogFile); n != len(files) {
		t.Errorf("log lines = %d, files on disk = %d", n, len(files))
	}
	if len(dirs) < len(cfg.FileExtensions) {
		t.Errorf("dirs on disk = %d, want at least %d top-level", len(dirs), len(cfg.FileExtensions))
	}

	fileSet := modeSet(fileModes)
	dirSet := modeSet(dirModes)
	for _, f := range files {
		info, err := os.Lstat(f)
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		if !fileSet[info.Mode().Perm()] {
			t.Errorf("%s: mode %o not in fileModes (umask leak or chmod fallback bug)", f, info.Mode().Perm())
		}
		checkTimes(t, f)
	}
	for _, d := range dirs {
		info, err := os.Lstat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if !dirSet[info.Mode().Perm()] {
			t.Errorf("%s: mode %o not in dirModes", d, info.Mode().Perm())
		}
		// Dir times are restamped in phase 3; child creation must not have
		// left "now" behind. (mtime only: the walk itself bumps dir atime.)
		checkMtime(t, d)
	}
}

func TestPopulateZeroFiles(t *testing.T) {
	setTestConfig(t)
	cfg.TargetFiles = 0

	if err := populateFilesystem(false); err != nil {
		t.Fatalf("populateFilesystem: %v", err)
	}
	files, _ := walkTree(t, cfg.BaseDir)
	if len(files) != 0 {
		t.Errorf("files on disk = %d, want 0", len(files))
	}
}

// TestPopulateLogOpenFailureCompletes guards the done-channel deadlock: when
// the log file can't be opened, populate must still create files and return.
func TestPopulateLogOpenFailureCompletes(t *testing.T) {
	setTestConfig(t)
	cfg.TargetDirs = 10
	cfg.TargetFiles = 60
	cfg.LogFile = filepath.Join(cfg.BaseDir, "missing-subdir", "files.log")

	doneCh := make(chan error, 1)
	go func() { doneCh <- populateFilesystem(false) }()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("populateFilesystem: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("populateFilesystem hung after log open failure (done-channel deadlock)")
	}

	files, _ := walkTree(t, cfg.BaseDir)
	if len(files) != cfg.TargetFiles {
		t.Errorf("files on disk = %d, want %d", len(files), cfg.TargetFiles)
	}
}

func TestTortureParallelDirs(t *testing.T) {
	setTestConfig(t)
	cfg.Torture.FlatDirs = []string{"flat_a", "flat_b", "flat_c"}
	cfg.Torture.FilesPerDir = 400
	cfg.Torture.FileSizeBytes = 64
	cfg.Torture.SkipMetadata = false
	cfg.Torture.ReportInterval = 1 << 30

	if err := populateTorture(); err != nil {
		t.Fatalf("populateTorture: %v", err)
	}

	fileSet := modeSet(fileModes)
	var samples [][]byte
	for _, d := range cfg.Torture.FlatDirs {
		dirPath := filepath.Join(cfg.BaseDir, d)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			t.Fatalf("readdir %s: %v", dirPath, err)
		}
		if len(entries) != int(cfg.Torture.FilesPerDir) {
			t.Errorf("%s: %d files, want exactly %d", dirPath, len(entries), cfg.Torture.FilesPerDir)
		}
		first := filepath.Join(dirPath, entries[0].Name())
		info, err := os.Lstat(first)
		if err != nil {
			t.Fatalf("stat %s: %v", first, err)
		}
		if !fileSet[info.Mode().Perm()] {
			t.Errorf("%s: mode %o not in fileModes", first, info.Mode().Perm())
		}
		if info.Size() != int64(cfg.Torture.FileSizeBytes) {
			t.Errorf("%s: size %d, want %d", first, info.Size(), cfg.Torture.FileSizeBytes)
		}
		checkTimes(t, first)

		data, err := os.ReadFile(first)
		if err != nil {
			t.Fatalf("read %s: %v", first, err)
		}
		samples = append(samples, data)
	}

	// Content must differ between files (identical content would let
	// dedup-capable storage cheat the test).
	identical := true
	for i := 1; i < len(samples); i++ {
		if string(samples[i]) != string(samples[0]) {
			identical = false
			break
		}
	}
	if identical {
		t.Error("all sampled torture files have identical content (dedup-friendly)")
	}
}

func TestDeepChain(t *testing.T) {
	setTestConfig(t)
	cfg.Deep.Depth = 25
	cfg.Deep.FilesPerLevel = 2
	cfg.Deep.FileSizeBytes = 128
	cfg.Deep.SkipMetadata = false
	cfg.Deep.ReportInterval = 1 << 30

	if err := populateDeep(); err != nil {
		t.Fatalf("populateDeep: %v", err)
	}

	path := cfg.BaseDir
	for level := 1; level <= cfg.Deep.Depth; level++ {
		path = filepath.Join(path, fmt.Sprintf("l%03d", level))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("level %d missing: %v", level, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", path)
		}
		checkMtime(t, path)
	}

	files, dirs := walkTree(t, cfg.BaseDir)
	if want := cfg.Deep.Depth * cfg.Deep.FilesPerLevel; len(files) != want {
		t.Errorf("files on disk = %d, want %d", len(files), want)
	}
	if len(dirs) != cfg.Deep.Depth {
		t.Errorf("dirs on disk = %d, want %d", len(dirs), cfg.Deep.Depth)
	}
}

func TestValidateConfig(t *testing.T) {
	valid := func() {
		setTestConfig(t)
	}

	valid()
	if err := validateConfig(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	valid()
	cfg.FileSize.MinNormal = 100
	cfg.FileSize.MaxNormal = 50
	if err := validateConfig(); err == nil {
		t.Error("max_normal < min_normal accepted; would panic in rand.IntN mid-run")
	}

	valid()
	cfg.FileSize.LargeFileChance = 5
	cfg.FileSize.MinLarge = 1000
	cfg.FileSize.MaxLarge = 10
	if err := validateConfig(); err == nil {
		t.Error("max_large < min_large accepted with large_file_chance > 0")
	}

	valid()
	cfg.FileSize.LargeFileChance = -1
	if err := validateConfig(); err == nil {
		t.Error("negative large_file_chance accepted")
	}

	valid()
	cfg.MaxSubdirsPerDir = -1
	if err := validateConfig(); err == nil {
		t.Error("negative max_subdirs_per_dir accepted; would panic in rand.IntN")
	}

	valid()
	cfg.Torture.FileSizeBytes = -1
	if err := validateConfig(); err == nil {
		t.Error("negative torture.file_size_bytes accepted; would panic in make()")
	}

	valid()
	cfg.BaseDir = ""
	if err := validateConfig(); err == nil {
		t.Error("empty base_dir accepted")
	}
}
