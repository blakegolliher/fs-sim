package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration parameters loaded from YAML
type Config struct {
	BaseDir               string `yaml:"base_dir"`
	LogFile               string `yaml:"log_file"`
	TargetDirs            int    `yaml:"target_dirs"`
	TargetFiles           int    `yaml:"target_files"`
	UpdateDurationSeconds int    `yaml:"update_duration_seconds"`
	MinFileAge            int64  `yaml:"min_file_age"`
	MaxFileAge            int64  `yaml:"max_file_age"`
	UIDs                  []int  `yaml:"uids"`
	GIDs                  []int  `yaml:"gids"`
	Workers               int    `yaml:"workers"` // Number of parallel workers
	ActionWeights         struct {
		CreateFile     int `yaml:"create_file"`
		DeleteFile     int `yaml:"delete_file"`
		AppendFile     int `yaml:"append_file"`
		ChangeMetadata int `yaml:"change_metadata"`
		TouchFile      int `yaml:"touch_file"`
	} `yaml:"action_weights"`
	FileSize struct {
		MinNormal       int `yaml:"min_normal"`
		MaxNormal       int `yaml:"max_normal"`
		LargeFileChance int `yaml:"large_file_chance"`
		MinLarge        int `yaml:"min_large"`
		MaxLarge        int `yaml:"max_large"`
	} `yaml:"file_size"`
	MaxDepth         int                 `yaml:"max_depth"`
	MaxSubdirsPerDir int                 `yaml:"max_subdirs_per_dir"`
	FileExtensions   map[string][]string `yaml:"file_extensions"`

	// Torture mode config - for creating flat directories with massive file counts
	Torture struct {
		FlatDirs       []string `yaml:"flat_dirs"`       // List of flat directory names to create
		FilesPerDir    int64    `yaml:"files_per_dir"`   // Number of files per flat directory
		FileSizeBytes  int      `yaml:"file_size_bytes"` // Fixed file size in bytes
		SkipMetadata   bool     `yaml:"skip_metadata"`   // Skip chown/chmod for speed
		ReportInterval int64    `yaml:"report_interval"` // Progress report every N files
	} `yaml:"torture"`

	// Deep mode config - for creating narrow-deep directory chains
	Deep struct {
		Depth          int  `yaml:"depth"`           // Number of nested directory levels
		FilesPerLevel  int  `yaml:"files_per_level"` // Number of files at each level
		FileSizeBytes  int  `yaml:"file_size_bytes"` // Fixed file size in bytes
		SkipMetadata   bool `yaml:"skip_metadata"`   // Skip chown/chmod for speed
		ReportInterval int  `yaml:"report_interval"` // Progress report every N levels
	} `yaml:"deep"`
}

// Global config
var cfg Config

// Metadata fast-path flags, computed once after config load.
var (
	// metaSkipChown is true when we're not euid 0 — chown would silently fail,
	// so skip the syscall entirely.
	metaSkipChown bool
	// metaSingleUID/metaSingleGID short-circuit getRandomID lookups.
	metaSingleUID bool
	metaSingleGID bool
	// metaTimeRange caches MaxFileAge - MinFileAge (0 means "use time.Now()").
	metaTimeRange int64
)

func initMetadataFastPath() {
	metaSkipChown = os.Geteuid() != 0
	metaSingleUID = len(cfg.UIDs) == 1
	metaSingleGID = len(cfg.GIDs) == 1
	if cfg.MaxFileAge > cfg.MinFileAge {
		metaTimeRange = cfg.MaxFileAge - cfg.MinFileAge
	}
}

// loadConfig reads and parses the YAML configuration file
func loadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set default max file age to current time if not specified
	if cfg.MaxFileAge == 0 {
		cfg.MaxFileAge = time.Now().Unix()
	}

	// Set default workers to CPU count if not specified
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.NumCPU()
	}

	// Validate required fields
	if cfg.BaseDir == "" {
		return fmt.Errorf("base_dir is required in config")
	}
	if cfg.LogFile == "" {
		return fmt.Errorf("log_file is required in config")
	}
	if len(cfg.UIDs) == 0 {
		return fmt.Errorf("at least one UID is required in config")
	}
	if len(cfg.GIDs) == 0 {
		return fmt.Errorf("at least one GID is required in config")
	}
	if len(cfg.FileExtensions) == 0 {
		return fmt.Errorf("file_extensions must be defined in config")
	}

	initMetadataFastPath()
	return nil
}

// getRandomID selects a random UID or GID from the provided list
func getRandomID(ids []int) int {
	return ids[rand.IntN(len(ids))]
}

// topLevelDirNames returns the top-level directory names, which are the keys
// of the file_extensions config.
func topLevelDirNames() []string {
	dirs := make([]string, 0, len(cfg.FileExtensions))
	for dir := range cfg.FileExtensions {
		dirs = append(dirs, dir)
	}
	return dirs
}

// nowStamp returns the current time formatted for progress lines.
func nowStamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// fileModes / dirModes are the permission sets randomly assigned to files and
// directories. Dir perms always include owner rwx (0700) so subsequent
// children can be created inside.
var (
	fileModes = []os.FileMode{0755, 0644, 0770, 0400, 0666, 0555}
	dirModes  = []os.FileMode{0755, 0775, 0770, 0750, 0700, 0777}
)

// getRandomPerms selects a random file permission mode.
func getRandomPerms() os.FileMode {
	return fileModes[rand.IntN(len(fileModes))]
}

// getRandomDirPerms selects a random directory permission mode.
func getRandomDirPerms() os.FileMode {
	return dirModes[rand.IntN(len(dirModes))]
}

// getRandomTime generates a random time between MinFileAge and MaxFileAge.
// Uses the precomputed metaTimeRange to skip subtraction per call.
func getRandomTime() time.Time {
	if metaTimeRange == 0 {
		return time.Now()
	}
	return time.Unix(rand.Int64N(metaTimeRange)+cfg.MinFileAge, 0)
}

// setMetadata sets random ownership, permissions, and historical atime/mtime.
// Uses precomputed fast-path flags to skip syscalls that would fail or are
// no-ops (e.g. chown when not running as root). Best-effort: individual
// syscall errors are intentionally ignored.
func setMetadata(path string, isDir bool) {
	// 1. Set Owner/Group — skip entirely when not root since it would just
	// silently fail. Avoid getRandomID when there's only one uid/gid.
	if !metaSkipChown {
		uid := cfg.UIDs[0]
		if !metaSingleUID {
			uid = getRandomID(cfg.UIDs)
		}
		gid := cfg.GIDs[0]
		if !metaSingleGID {
			gid = getRandomID(cfg.GIDs)
		}
		os.Chown(path, uid, gid)
	}

	// 2. Set Permissions
	var mode os.FileMode
	if isDir {
		mode = getRandomDirPerms()
	} else {
		mode = getRandomPerms()
	}
	os.Chmod(path, mode)

	// 3. Set Historical Access/Modification Times (independent atime and mtime)
	atime := getRandomTime()
	mtime := getRandomTime()
	os.Chtimes(path, atime, mtime)
}

// FastRandom provides fast random byte generation using a pre-filled buffer
type FastRandom struct {
	buffer []byte
	pos    int
}

// NewFastRandom creates a new fast random generator with a large pre-filled buffer
func NewFastRandom(size int) *FastRandom {
	fr := &FastRandom{
		buffer: make([]byte, size),
		pos:    0,
	}
	// Fill buffer with random non-zero bytes
	for i := range fr.buffer {
		fr.buffer[i] = byte(rand.IntN(255) + 1)
	}
	return fr
}

// Fill fills the destination slice with random bytes from the buffer
// using copy() instead of a per-byte loop.
func (fr *FastRandom) Fill(dst []byte) {
	bufLen := len(fr.buffer)
	remaining := len(dst)
	written := 0
	for remaining > 0 {
		if fr.pos >= bufLen {
			fr.pos = 0
		}
		n := copy(dst[written:], fr.buffer[fr.pos:])
		fr.pos += n
		written += n
		remaining -= n
	}
}

// writeRandomDataFast writes random bytes using a caller-supplied reusable
// chunk buffer. Files that fit in the chunk get a single fill+write; larger
// files are written in chunk-sized pieces.
func writeRandomDataFast(filePath string, size int, fr *FastRandom, chunk []byte) error {
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	if size <= len(chunk) {
		fr.Fill(chunk[:size])
		_, err := f.Write(chunk[:size])
		return err
	}

	remaining := size
	for remaining > 0 {
		writeSize := len(chunk)
		if remaining < writeSize {
			writeSize = remaining
		}
		fr.Fill(chunk[:writeSize])
		if _, err := f.Write(chunk[:writeSize]); err != nil {
			return err
		}
		remaining -= writeSize
	}
	return nil
}

// getExtensionForCategory returns a random file extension for the given category
func getExtensionForCategory(category string) string {
	exts, ok := cfg.FileExtensions[category]
	if !ok || len(exts) == 0 {
		return ".dat"
	}
	return exts[rand.IntN(len(exts))]
}

// getCategoryForPath determines the file category based on path
func getCategoryForPath(path string) string {
	if strings.Contains(path, "/scratch") {
		return "scratch"
	} else if strings.Contains(path, "/data") {
		return "data"
	} else if strings.Contains(path, "/web") {
		return "web"
	}
	return "home"
}

// FileJob represents a file to be created
type FileJob struct {
	Path     string
	Category string
	FileNum  int
}

// estimateDirCount returns (avgEstimate, maxPossible) directory counts the
// BFS tree-build can reach for the given top-level count, max_subdirs_per_dir,
// and max_depth — clamped to TargetDirs. Each parent generates rand.IntN(K+1)
// children, so the average fanout is K/2 (rounded down, min 1 when K>=1).
func estimateDirCount(topLevel, maxSubdirs, maxDepth int, target int64) (int64, int64) {
	if topLevel <= 0 || maxDepth <= 0 {
		return int64(topLevel), int64(topLevel)
	}
	avgK := int64(maxSubdirs / 2)
	if avgK < 1 && maxSubdirs >= 1 {
		avgK = 1
	}
	maxK := int64(maxSubdirs)

	avgTotal := int64(topLevel)
	maxTotal := int64(topLevel)
	avgLevel := int64(topLevel)
	maxLevel := int64(topLevel)
	const cap = int64(1) << 60
	for level := 1; level < maxDepth; level++ {
		avgLevel *= avgK
		maxLevel *= maxK
		if avgLevel > cap {
			avgLevel = cap
		}
		if maxLevel > cap {
			maxLevel = cap
		}
		avgTotal += avgLevel
		maxTotal += maxLevel
		if avgTotal > target && maxTotal > target {
			break
		}
	}
	if avgTotal > target {
		avgTotal = target
	}
	if maxTotal > target {
		maxTotal = target
	}
	return avgTotal, maxTotal
}

// populateFilesystem creates the initial directory structure and files
func populateFilesystem() error {
	fmt.Println("--- Starting initial filesystem population ---")
	fmt.Printf("Targets: %d files, up to %d directories\n", cfg.TargetFiles, cfg.TargetDirs)
	fmt.Printf("Base directory: %s\n", cfg.BaseDir)
	fmt.Printf("Workers: %d\n", cfg.Workers)
	startTime := time.Now()

	// Ensure base directory exists
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	// Get top-level directory names from file extensions config
	topLevelDirs := topLevelDirNames()

	// Tell the user up front what tree shape can actually produce, so a
	// target_dirs that exceeds the achievable maximum is obvious before
	// Phase 1 begins.
	avgEst, maxEst := estimateDirCount(len(topLevelDirs), cfg.MaxSubdirsPerDir, cfg.MaxDepth, int64(cfg.TargetDirs))
	fmt.Printf("Tree shape: max_depth=%d, max_subdirs_per_dir=%d, top-level=%d -> ~%d dirs avg, %d dirs max\n",
		cfg.MaxDepth, cfg.MaxSubdirsPerDir, len(topLevelDirs), avgEst, maxEst)
	if int64(cfg.TargetDirs) > maxEst {
		fmt.Printf("NOTE: target_dirs=%d exceeds the achievable max (%d). Increase max_depth or max_subdirs_per_dir to grow more dirs.\n",
			cfg.TargetDirs, maxEst)
	}

	// Create top-level directories
	for _, dir := range topLevelDirs {
		path := filepath.Join(cfg.BaseDir, dir)
		if err := os.MkdirAll(path, 0755); err != nil {
			fmt.Printf("Warning: Could not create %s: %v\n", path, err)
		}
		setMetadata(path, true)
	}

	// Phase 1: Create all directories in parallel, level-by-level (BFS).
	// At each depth, parents are processed concurrently by cfg.Workers
	// goroutines, each calling os.Mkdir + setMetadata.
	fmt.Println("Phase 1: Creating directory structure (parallel)...")
	allDirs := make([]string, 0, cfg.TargetDirs)
	for _, dir := range topLevelDirs {
		allDirs = append(allDirs, filepath.Join(cfg.BaseDir, dir))
	}

	var totalDirs atomic.Int64
	totalDirs.Store(int64(len(topLevelDirs)))
	target := int64(cfg.TargetDirs)

	currentLevel := make([]string, len(allDirs))
	copy(currentLevel, allDirs)

	for parentDepth := 1; parentDepth < cfg.MaxDepth && totalDirs.Load() < target && len(currentLevel) > 0; parentDepth++ {
		parents := make(chan string, len(currentLevel))
		for _, p := range currentLevel {
			parents <- p
		}
		close(parents)

		var nextLevel []string
		var nextMu sync.Mutex
		var dirWg sync.WaitGroup

		for w := 0; w < cfg.Workers; w++ {
			dirWg.Add(1)
			go func() {
				defer dirWg.Done()
				local := make([]string, 0, 256)
				for parentDir := range parents {
					if totalDirs.Load() >= target {
						continue
					}
					numSubDirs := rand.IntN(cfg.MaxSubdirsPerDir + 1)
					for i := 0; i < numSubDirs; i++ {
						counter := totalDirs.Add(1)
						if counter > target {
							break
						}
						dirName := fmt.Sprintf("d_%03d_%d", rand.IntN(999), counter)
						newDirPath := filepath.Join(parentDir, dirName)
						if err := os.Mkdir(newDirPath, 0755); err != nil {
							continue
						}
						setMetadata(newDirPath, true)
						local = append(local, newDirPath)
					}
				}
				if len(local) > 0 {
					nextMu.Lock()
					nextLevel = append(nextLevel, local...)
					nextMu.Unlock()
				}
			}()
		}
		dirWg.Wait()

		allDirs = append(allDirs, nextLevel...)
		fmt.Printf("  [%s] Depth %d: %d directories total\n",
			nowStamp(), parentDepth+1, totalDirs.Load())
		currentLevel = nextLevel
	}

	currentDirs := int(totalDirs.Load())
	if currentDirs > cfg.TargetDirs {
		currentDirs = cfg.TargetDirs
	}
	if currentDirs < cfg.TargetDirs {
		fmt.Printf("Phase 1 complete: %d directories created (below target %d - tree shape capped growth at depth %d)\n",
			currentDirs, cfg.TargetDirs, cfg.MaxDepth)
	} else {
		fmt.Printf("Phase 1 complete: %d directories created\n", currentDirs)
	}

	// Phase 2: Create files in parallel
	fmt.Println("Phase 2: Creating files with parallel workers...")

	// Channel for file jobs
	jobs := make(chan FileJob, cfg.Workers*100)
	// Channel for completed file path batches (for logging).
	// Batching cuts per-file channel send overhead by ~256x.
	results := make(chan []string, cfg.Workers*4)
	// Done channel
	done := make(chan struct{})

	var fileCount atomic.Int64
	var wg sync.WaitGroup

	// Start log writer goroutine
	go func() {
		logFile, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Printf("Warning: Could not open log file: %v\n", err)
			// Drain results channel
			for range results {
			}
			return
		}
		defer logFile.Close()

		writer := bufio.NewWriterSize(logFile, 1024*1024)
		for batch := range results {
			for _, path := range batch {
				writer.WriteString(path)
				writer.WriteByte('\n')
			}
		}
		writer.Flush()
		close(done)
	}()

	// Start worker goroutines
	const logBatchSize = 256
	for w := 0; w < cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker gets its own fast random generator (1MB buffer)
			// and a reusable write chunk to avoid per-file allocations.
			fr := NewFastRandom(1024 * 1024)
			chunk := make([]byte, 64*1024)
			batch := make([]string, 0, logBatchSize)

			for job := range jobs {
				ext := getExtensionForCategory(job.Category)
				fileName := fmt.Sprintf("f_%06d%s", job.FileNum, ext)
				filePath := filepath.Join(job.Path, fileName)

				// Determine file size
				fileSize := rand.IntN(cfg.FileSize.MaxNormal-cfg.FileSize.MinNormal+1) + cfg.FileSize.MinNormal
				if rand.IntN(100) < cfg.FileSize.LargeFileChance {
					fileSize = rand.IntN(cfg.FileSize.MaxLarge-cfg.FileSize.MinLarge+1) + cfg.FileSize.MinLarge
				}

				if err := writeRandomDataFast(filePath, fileSize, fr, chunk); err != nil {
					continue
				}
				setMetadata(filePath, false)

				batch = append(batch, filePath)
				if len(batch) >= logBatchSize {
					results <- batch
					batch = make([]string, 0, logBatchSize)
				}

				count := fileCount.Add(1)
				if count%100000 == 0 {
					elapsed := time.Since(startTime)
					rate := float64(count) / elapsed.Seconds()
					fmt.Printf("  [%s] Progress: %d files (%.0f files/sec)\n",
						nowStamp(), count, rate)
				}
			}
			if len(batch) > 0 {
				results <- batch
			}
		}()
	}

	// Generate file jobs - distribute files across directories
	go func() {
		fileNum := 0
		filesPerDir := cfg.TargetFiles / len(allDirs)
		if filesPerDir < 1 {
			filesPerDir = 1
		}
		extraFiles := cfg.TargetFiles % len(allDirs)

		for i, dir := range allDirs {
			if fileNum >= cfg.TargetFiles {
				break
			}

			category := getCategoryForPath(dir)
			numFiles := filesPerDir
			if i < extraFiles {
				numFiles++
			}
			// Add some randomness
			numFiles = numFiles + rand.IntN(5) - 2
			if numFiles < 1 {
				numFiles = 1
			}

			for j := 0; j < numFiles && fileNum < cfg.TargetFiles; j++ {
				jobs <- FileJob{
					Path:     dir,
					Category: category,
					FileNum:  fileNum,
				}
				fileNum++
			}
		}
		close(jobs)
	}()

	// Wait for all workers to complete
	wg.Wait()
	close(results)
	<-done

	finalCount := fileCount.Load()
	finalElapsed := time.Since(startTime)
	rate := float64(finalCount) / finalElapsed.Seconds()

	fmt.Printf("\n--- Population Complete ---\n")
	fmt.Printf("Total Files: %d\n", finalCount)
	fmt.Printf("Total Directories: %d\n", currentDirs)
	fmt.Printf("Time Taken: %s\n", finalElapsed.Round(time.Second))
	fmt.Printf("Average Rate: %.0f files/sec\n", rate)

	return nil
}

// TortureJob represents a range of files to create in a directory
type TortureJob struct {
	DirPath   string
	StartFile int64
	EndFile   int64
}

// populateTorture creates flat directories with massive file counts for stress testing
func populateTorture() error {
	fmt.Println("=== TORTURE MODE: Flat Directory Stress Test ===")
	fmt.Printf("Target: %d files per directory\n", cfg.Torture.FilesPerDir)
	fmt.Printf("Directories: %v\n", cfg.Torture.FlatDirs)
	fmt.Printf("File size: %d bytes\n", cfg.Torture.FileSizeBytes)
	fmt.Printf("Workers: %d\n", cfg.Workers)
	fmt.Printf("Skip metadata: %v\n", cfg.Torture.SkipMetadata)
	fmt.Println()

	if len(cfg.Torture.FlatDirs) == 0 {
		return fmt.Errorf("torture.flat_dirs must be specified")
	}
	if cfg.Torture.FilesPerDir <= 0 {
		return fmt.Errorf("torture.files_per_dir must be positive")
	}

	// Default report interval
	reportInterval := cfg.Torture.ReportInterval
	if reportInterval <= 0 {
		reportInterval = 1000000 // 1M files
	}

	// Ensure base directory exists
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	totalStartTime := time.Now()
	var totalFiles int64

	// Process each flat directory
	for dirIdx, dirName := range cfg.Torture.FlatDirs {
		dirPath := filepath.Join(cfg.BaseDir, dirName)
		fmt.Printf("\n--- Directory %d/%d: %s ---\n", dirIdx+1, len(cfg.Torture.FlatDirs), dirPath)

		// Create the directory
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dirPath, err)
		}

		dirStartTime := time.Now()
		var fileCount atomic.Int64

		// Channel for file creation jobs (batched by range)
		jobs := make(chan TortureJob, cfg.Workers*2)
		var wg sync.WaitGroup

		// Start worker goroutines
		for w := 0; w < cfg.Workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Each worker gets its own random buffer
				fr := NewFastRandom(64 * 1024)
				content := make([]byte, cfg.Torture.FileSizeBytes)
				fr.Fill(content)

				for job := range jobs {
					for fileNum := job.StartFile; fileNum < job.EndFile; fileNum++ {
						// Simple numeric filename: f_000000000001.dat
						fileName := fmt.Sprintf("f_%012d.dat", fileNum)
						filePath := filepath.Join(job.DirPath, fileName)

						// Create file with pre-filled content
						f, err := os.Create(filePath)
						if err != nil {
							continue
						}
						f.Write(content)
						f.Close()

						if !cfg.Torture.SkipMetadata {
							setMetadata(filePath, false)
						}

						count := fileCount.Add(1)
						if count%reportInterval == 0 {
							elapsed := time.Since(dirStartTime)
							rate := float64(count) / elapsed.Seconds()
							pct := float64(count) / float64(cfg.Torture.FilesPerDir) * 100
							fmt.Printf("  [%s] Progress: %d files (%.1f%%) - %.0f files/sec\n",
								nowStamp(), count, pct, rate)
						}
					}
				}
			}()
		}

		// Generate jobs - batch files into chunks
		go func() {
			batchSize := int64(10000) // 10K files per job
			for start := int64(0); start < cfg.Torture.FilesPerDir; start += batchSize {
				end := start + batchSize
				if end > cfg.Torture.FilesPerDir {
					end = cfg.Torture.FilesPerDir
				}
				jobs <- TortureJob{
					DirPath:   dirPath,
					StartFile: start,
					EndFile:   end,
				}
			}
			close(jobs)
		}()

		// Wait for all workers
		wg.Wait()

		dirElapsed := time.Since(dirStartTime)
		dirCount := fileCount.Load()
		dirRate := float64(dirCount) / dirElapsed.Seconds()
		totalFiles += dirCount

		fmt.Printf("  Directory complete: %d files in %s (%.0f files/sec)\n",
			dirCount, dirElapsed.Round(time.Second), dirRate)
	}

	totalElapsed := time.Since(totalStartTime)
	totalRate := float64(totalFiles) / totalElapsed.Seconds()

	fmt.Printf("\n=== TORTURE MODE COMPLETE ===\n")
	fmt.Printf("Total Files: %d\n", totalFiles)
	fmt.Printf("Total Directories: %d\n", len(cfg.Torture.FlatDirs))
	fmt.Printf("Total Time: %s\n", totalElapsed.Round(time.Second))
	fmt.Printf("Overall Rate: %.0f files/sec\n", totalRate)

	// Calculate capacity used
	capacityBytes := totalFiles * int64(cfg.Torture.FileSizeBytes)
	capacityGB := float64(capacityBytes) / (1024 * 1024 * 1024)
	fmt.Printf("Capacity Used: %.2f GB\n", capacityGB)

	return nil
}

// populateDeep creates a narrow-deep directory chain for benchmarking recursion
func populateDeep() error {
	fmt.Println("=== DEEP MODE: Narrow-Deep Directory Chain ===")
	fmt.Printf("Target depth: %d levels\n", cfg.Deep.Depth)
	fmt.Printf("Files per level: %d\n", cfg.Deep.FilesPerLevel)
	fmt.Printf("File size: %d bytes\n", cfg.Deep.FileSizeBytes)
	fmt.Printf("Skip metadata: %v\n", cfg.Deep.SkipMetadata)
	fmt.Println()

	if cfg.Deep.Depth <= 0 {
		return fmt.Errorf("deep.depth must be positive")
	}
	if cfg.Deep.FilesPerLevel < 0 {
		return fmt.Errorf("deep.files_per_level must be non-negative")
	}

	// Default file size
	fileSize := cfg.Deep.FileSizeBytes
	if fileSize <= 0 {
		fileSize = 1024 // 1KB default
	}

	// Default report interval
	reportInterval := cfg.Deep.ReportInterval
	if reportInterval <= 0 {
		reportInterval = 100
	}

	startTime := time.Now()

	// Ensure base directory exists
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	// Create random data buffer
	fr := NewFastRandom(64 * 1024)
	content := make([]byte, fileSize)
	fr.Fill(content)

	currentPath := cfg.BaseDir
	totalFiles := 0

	for level := 1; level <= cfg.Deep.Depth; level++ {
		// Create directory for this level (short name to avoid PATH_MAX)
		dirName := fmt.Sprintf("l%03d", level)
		currentPath = filepath.Join(currentPath, dirName)

		if err := os.Mkdir(currentPath, 0755); err != nil {
			return fmt.Errorf("failed to create directory at level %d: %w", level, err)
		}

		if !cfg.Deep.SkipMetadata {
			setMetadata(currentPath, true)
		}

		// Create files at this level (short name to avoid PATH_MAX)
		for f := 1; f <= cfg.Deep.FilesPerLevel; f++ {
			fileName := fmt.Sprintf("f%03d.dat", f)
			filePath := filepath.Join(currentPath, fileName)

			file, err := os.Create(filePath)
			if err != nil {
				return fmt.Errorf("failed to create file at level %d: %w", level, err)
			}
			file.Write(content)
			file.Close()

			if !cfg.Deep.SkipMetadata {
				setMetadata(filePath, false)
			}

			totalFiles++
		}

		// Progress report
		if level%reportInterval == 0 {
			elapsed := time.Since(startTime)
			fmt.Printf("  Level %d/%d - %d files (%.1f sec)\n",
				level, cfg.Deep.Depth, totalFiles, elapsed.Seconds())
		}
	}

	elapsed := time.Since(startTime)
	totalDirs := cfg.Deep.Depth

	fmt.Printf("\n=== DEEP MODE COMPLETE ===\n")
	fmt.Printf("Total Levels: %d\n", totalDirs)
	fmt.Printf("Total Files: %d\n", totalFiles)
	fmt.Printf("Total Time: %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("Deepest Path: %s\n", currentPath)

	// Calculate capacity
	capacityBytes := int64(totalFiles) * int64(fileSize)
	capacityKB := float64(capacityBytes) / 1024
	fmt.Printf("Capacity Used: %.2f KB\n", capacityKB)

	return nil
}

// FileIndex provides efficient random file selection from the log file
type FileIndex struct {
	paths     []string
	deletions map[string]bool
	mu        sync.RWMutex
}

// NewFileIndex loads the file index from the log file
func NewFileIndex() (*FileIndex, error) {
	file, err := os.Open(cfg.LogFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	idx := &FileIndex{
		paths:     make([]string, 0, 100000),
		deletions: make(map[string]bool),
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 0 {
			idx.paths = append(idx.paths, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading log file: %w", err)
	}

	if len(idx.paths) == 0 {
		return nil, fmt.Errorf("log file is empty")
	}

	fmt.Printf("Loaded %d file paths from log\n", len(idx.paths))
	return idx, nil
}

// SelectRandom returns a random file path that hasn't been deleted
func (idx *FileIndex) SelectRandom() (string, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	for attempts := 0; attempts < 100; attempts++ {
		path := idx.paths[rand.IntN(len(idx.paths))]
		if !idx.deletions[path] {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("could not find a valid file after 100 attempts")
}

// MarkDeleted marks a file as deleted in the index
func (idx *FileIndex) MarkDeleted(path string) {
	idx.mu.Lock()
	idx.deletions[path] = true
	idx.mu.Unlock()
}

// AddPath adds a new path to the index
func (idx *FileIndex) AddPath(path string) {
	idx.mu.Lock()
	idx.paths = append(idx.paths, path)
	idx.mu.Unlock()
}

// SaveDeletions writes the updated log file, removing deleted entries
func (idx *FileIndex) SaveDeletions() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.deletions) == 0 {
		return nil
	}

	file, err := os.Create(cfg.LogFile)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, path := range idx.paths {
		if !idx.deletions[path] {
			writer.WriteString(path + "\n")
		}
	}
	return writer.Flush()
}

// deleteRandomFile removes a file and marks it in the index
func deleteRandomFile(idx *FileIndex) error {
	filePath, err := idx.SelectRandom()
	if err != nil {
		return err
	}

	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("failed to delete file %s: %w", filePath, err)
	}

	idx.MarkDeleted(filePath)
	fmt.Printf("DELETED: %s\n", filePath)
	return nil
}

// appendToFile appends a small amount of data to a random file
func appendToFile(idx *FileIndex) error {
	filePath, err := idx.SelectRandom()
	if err != nil {
		return err
	}

	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file %s for append: %w", filePath, err)
	}
	defer f.Close()

	size := rand.IntN(100) + 10
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(rand.IntN(255) + 1)
	}

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to append to file %s: %w", filePath, err)
	}

	fmt.Printf("APPENDED: %s (+%d bytes)\n", filePath, size)
	return nil
}

// changeMetadata changes the UID, GID, and permissions of a random file
func changeMetadata(idx *FileIndex) error {
	filePath, err := idx.SelectRandom()
	if err != nil {
		return err
	}

	setMetadata(filePath, false)

	fmt.Printf("METADATA CHANGED: %s\n", filePath)
	return nil
}

// createNewFile adds a new file to a random directory
func createNewFile(idx *FileIndex) error {
	topLevelDirs := topLevelDirNames()

	dirName := topLevelDirs[rand.IntN(len(topLevelDirs))]
	targetDir := filepath.Join(cfg.BaseDir, dirName)

	ext := getExtensionForCategory(dirName)
	fileName := fmt.Sprintf("new_f_%d_%s%s", time.Now().UnixNano(), dirName, ext)
	filePath := filepath.Join(targetDir, fileName)

	fileSize := rand.IntN(9216) + 1024

	fr := NewFastRandom(64 * 1024)
	chunk := make([]byte, 64*1024)
	if err := writeRandomDataFast(filePath, fileSize, fr, chunk); err != nil {
		return fmt.Errorf("failed to write new file %s: %w", filePath, err)
	}

	setMetadata(filePath, false)

	logFile, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		logFile.WriteString(filePath + "\n")
		logFile.Close()
	}

	idx.AddPath(filePath)
	fmt.Printf("CREATED: %s\n", filePath)
	return nil
}

// touchFile updates the mtime and atime of a random file to the current time
func touchFile(idx *FileIndex) error {
	filePath, err := idx.SelectRandom()
	if err != nil {
		return err
	}

	now := time.Now()
	if err := os.Chtimes(filePath, now, now); err != nil {
		return fmt.Errorf("failed to touch file %s: %w", filePath, err)
	}

	fmt.Printf("TOUCHED: %s\n", filePath)
	return nil
}

// runDynamicUpdate is the main function for the update mode
func runDynamicUpdate() error {
	duration := time.Duration(cfg.UpdateDurationSeconds) * time.Second
	fmt.Printf("--- Starting Dynamic Update Mode (Running for %s) ---\n", duration)

	idx, err := NewFileIndex()
	if err != nil {
		return fmt.Errorf("failed to load file index: %w", err)
	}
	defer idx.SaveDeletions()

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	type actionFunc func(*FileIndex) error

	actions := []struct {
		f      actionFunc
		weight int
		name   string
	}{
		{createNewFile, cfg.ActionWeights.CreateFile, "Create New File"},
		{deleteRandomFile, cfg.ActionWeights.DeleteFile, "Delete File"},
		{appendToFile, cfg.ActionWeights.AppendFile, "Append to File"},
		{changeMetadata, cfg.ActionWeights.ChangeMetadata, "Change Metadata"},
		{touchFile, cfg.ActionWeights.TouchFile, "Touch File"},
	}

	totalWeight := 0
	for _, action := range actions {
		totalWeight += action.weight
	}

	if totalWeight == 0 {
		return fmt.Errorf("total action weight is 0, check config")
	}

	actionCount := 0
	errorCount := 0

	for {
		select {
		case <-ctx.Done():
			fmt.Printf("\n--- Dynamic update complete ---\n")
			fmt.Printf("Actions performed: %d, Errors: %d\n", actionCount, errorCount)
			return nil
		default:
			r := rand.IntN(totalWeight)
			cumulativeWeight := 0
			var chosenAction actionFunc
			var chosenName string

			for _, action := range actions {
				cumulativeWeight += action.weight
				if r < cumulativeWeight {
					chosenAction = action.f
					chosenName = action.name
					break
				}
			}

			if chosenAction != nil {
				if err := chosenAction(idx); err != nil {
					// A file vanishing between selection and use is expected
					// churn, not an error worth reporting.
					if !errors.Is(err, os.ErrNotExist) {
						fmt.Printf("Action failed (%s): %v\n", chosenName, err)
						errorCount++
					}
				} else {
					actionCount++
				}
			}

			time.Sleep(time.Millisecond * time.Duration(rand.IntN(100)+50))
		}
	}
}

func main() {
	var configPath string
	var mode string

	flag.StringVar(&configPath, "config", "config.yaml", "Path to configuration file")
	flag.StringVar(&mode, "mode", "", "Mode: populate or update")
	flag.Parse()

	if mode == "" && flag.NArg() > 0 {
		mode = strings.ToLower(flag.Arg(0))
	}

	if mode == "" {
		fmt.Println("Usage: fs-sim [--config config.yaml] <mode>")
		fmt.Println("       fs-sim --mode=<mode> [--config=config.yaml]")
		fmt.Println("")
		fmt.Println("Modes:")
		fmt.Println("  populate  Create initial filesystem structure (hierarchical)")
		fmt.Println("  torture   Create flat directories with massive file counts")
		fmt.Println("  deep      Create narrow-deep directory chain (for recursion testing)")
		fmt.Println("  update    Run dynamic changes (for cron jobs)")
		fmt.Println("")
		fmt.Println("Options:")
		fmt.Println("  --config  Path to YAML configuration file (default: config.yaml)")
		os.Exit(1)
	}

	fmt.Printf("Loading configuration from: %s\n", configPath)
	if err := loadConfig(configPath); err != nil {
		fmt.Printf("FATAL: Failed to load config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Configuration loaded successfully\n\n")

	switch mode {
	case "populate":
		if err := populateFilesystem(); err != nil {
			fmt.Printf("FATAL POPULATE ERROR: %v\n", err)
			os.Exit(1)
		}
	case "torture":
		if err := populateTorture(); err != nil {
			fmt.Printf("FATAL TORTURE ERROR: %v\n", err)
			os.Exit(1)
		}
	case "deep":
		if err := populateDeep(); err != nil {
			fmt.Printf("FATAL DEEP ERROR: %v\n", err)
			os.Exit(1)
		}
	case "update":
		if err := runDynamicUpdate(); err != nil {
			fmt.Printf("FATAL UPDATE ERROR: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Invalid mode: %s. Use 'populate', 'torture', 'deep', or 'update'.\n", mode)
		os.Exit(1)
	}
}
