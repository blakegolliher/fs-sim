package main

import (
	"bufio"
	"context"
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
		FlatDirs        []string `yaml:"flat_dirs"`         // List of flat directory names to create
		FilesPerDir     int64    `yaml:"files_per_dir"`     // Number of files per flat directory
		FileSizeBytes   int      `yaml:"file_size_bytes"`   // Fixed file size in bytes
		SkipMetadata    bool     `yaml:"skip_metadata"`     // Skip chown/chmod for speed
		ReportInterval  int64    `yaml:"report_interval"`   // Progress report every N files
	} `yaml:"torture"`
}

// Global config
var cfg Config

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

	return nil
}

// getRandomID selects a random UID or GID from the provided list
func getRandomID(ids []int) int {
	return ids[rand.IntN(len(ids))]
}

// getRandomPerms selects a random file or directory permission mode
func getRandomPerms() os.FileMode {
	perms := []os.FileMode{0755, 0644, 0770, 0400, 0666, 0555}
	return perms[rand.IntN(len(perms))]
}

// getRandomTime generates a random time between MinFileAge and MaxFileAge
func getRandomTime() time.Time {
	if cfg.MaxFileAge <= cfg.MinFileAge {
		return time.Now()
	}
	randSec := rand.Int64N(cfg.MaxFileAge-cfg.MinFileAge) + cfg.MinFileAge
	return time.Unix(randSec, 0)
}

// setMetadata sets random ownership, permissions, and historical atime/mtime
func setMetadata(path string, isDir bool) error {
	uid := getRandomID(cfg.UIDs)
	gid := getRandomID(cfg.GIDs)

	// 1. Set Owner/Group (ignore errors, may not be root)
	os.Chown(path, uid, gid)

	// 2. Set Permissions
	var mode os.FileMode
	if isDir {
		baseMode := getRandomPerms()
		mode = baseMode | 0100
	} else {
		mode = getRandomPerms()
	}
	os.Chmod(path, mode)

	// 3. Set Historical Access/Modification Times
	randTime := getRandomTime()
	os.Chtimes(path, randTime, randTime)

	return nil
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
func (fr *FastRandom) Fill(dst []byte) {
	for i := range dst {
		dst[i] = fr.buffer[fr.pos]
		fr.pos = (fr.pos + 1) % len(fr.buffer)
	}
}

// writeRandomDataFast writes random bytes using pre-generated buffer
func writeRandomDataFast(filePath string, size int, fr *FastRandom) error {
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := bufio.NewWriterSize(f, 64*1024)

	const chunkSize = 64 * 1024
	chunk := make([]byte, chunkSize)
	remaining := size

	for remaining > 0 {
		writeSize := chunkSize
		if remaining < chunkSize {
			writeSize = remaining
		}
		fr.Fill(chunk[:writeSize])
		if _, err := writer.Write(chunk[:writeSize]); err != nil {
			return err
		}
		remaining -= writeSize
	}

	return writer.Flush()
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

// populateFilesystem creates the initial directory structure and files
func populateFilesystem() error {
	fmt.Println("--- Starting initial filesystem population ---")
	fmt.Printf("Target: %d files, %d directories\n", cfg.TargetFiles, cfg.TargetDirs)
	fmt.Printf("Base directory: %s\n", cfg.BaseDir)
	fmt.Printf("Workers: %d\n", cfg.Workers)
	startTime := time.Now()

	// Ensure base directory exists
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	// Get top-level directory names from file extensions config
	topLevelDirs := make([]string, 0, len(cfg.FileExtensions))
	for dir := range cfg.FileExtensions {
		topLevelDirs = append(topLevelDirs, dir)
	}

	// Create top-level directories
	for _, dir := range topLevelDirs {
		path := filepath.Join(cfg.BaseDir, dir)
		if err := os.MkdirAll(path, 0755); err != nil {
			fmt.Printf("Warning: Could not create %s: %v\n", path, err)
		}
		setMetadata(path, true)
	}

	// Phase 1: Create all directories first (single-threaded for simplicity)
	fmt.Println("Phase 1: Creating directory structure...")
	allDirs := make([]string, 0, cfg.TargetDirs)

	// Start with top-level dirs
	for _, dir := range topLevelDirs {
		allDirs = append(allDirs, filepath.Join(cfg.BaseDir, dir))
	}

	dirIndex := 0
	currentDirs := len(topLevelDirs)

	for currentDirs < cfg.TargetDirs && dirIndex < len(allDirs) {
		parentDir := allDirs[dirIndex]
		dirIndex++

		// Determine depth based on path
		depth := strings.Count(parentDir, string(os.PathSeparator)) - strings.Count(cfg.BaseDir, string(os.PathSeparator))
		if depth >= cfg.MaxDepth {
			continue
		}

		numSubDirs := rand.IntN(cfg.MaxSubdirsPerDir + 1)
		for i := 0; i < numSubDirs && currentDirs < cfg.TargetDirs; i++ {
			dirName := fmt.Sprintf("d_%03d_%d", rand.IntN(999), currentDirs)
			newDirPath := filepath.Join(parentDir, dirName)

			if err := os.Mkdir(newDirPath, 0755); err != nil {
				continue
			}
			setMetadata(newDirPath, true)
			allDirs = append(allDirs, newDirPath)
			currentDirs++

			if currentDirs%10000 == 0 {
				fmt.Printf("  Directories: %d/%d\n", currentDirs, cfg.TargetDirs)
			}
		}
	}

	fmt.Printf("Phase 1 complete: %d directories created\n", currentDirs)

	// Phase 2: Create files in parallel
	fmt.Println("Phase 2: Creating files with parallel workers...")

	// Channel for file jobs
	jobs := make(chan FileJob, cfg.Workers*100)
	// Channel for completed file paths (for logging)
	results := make(chan string, cfg.Workers*100)
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

		writer := bufio.NewWriterSize(logFile, 256*1024)
		for path := range results {
			writer.WriteString(path + "\n")
		}
		writer.Flush()
		close(done)
	}()

	// Start worker goroutines
	for w := 0; w < cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker gets its own fast random generator (1MB buffer)
			fr := NewFastRandom(1024 * 1024)

			for job := range jobs {
				ext := getExtensionForCategory(job.Category)
				fileName := fmt.Sprintf("f_%06d%s", job.FileNum, ext)
				filePath := filepath.Join(job.Path, fileName)

				// Determine file size
				fileSize := rand.IntN(cfg.FileSize.MaxNormal-cfg.FileSize.MinNormal+1) + cfg.FileSize.MinNormal
				if rand.IntN(100) < cfg.FileSize.LargeFileChance {
					fileSize = rand.IntN(cfg.FileSize.MaxLarge-cfg.FileSize.MinLarge+1) + cfg.FileSize.MinLarge
				}

				if err := writeRandomDataFast(filePath, fileSize, fr); err != nil {
					continue
				}
				setMetadata(filePath, false)

				results <- filePath

				count := fileCount.Add(1)
				if count%100000 == 0 {
					elapsed := time.Since(startTime)
					rate := float64(count) / elapsed.Seconds()
					fmt.Printf("  Progress: %d files (%.0f files/sec)\n", count, rate)
				}
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
							fmt.Printf("  Progress: %d files (%.1f%%) - %.0f files/sec\n", count, pct, rate)
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

	if err := setMetadata(filePath, false); err != nil {
		return fmt.Errorf("failed to change metadata for %s: %w", filePath, err)
	}

	fmt.Printf("METADATA CHANGED: %s\n", filePath)
	return nil
}

// createNewFile adds a new file to a random directory
func createNewFile(idx *FileIndex) error {
	topLevelDirs := make([]string, 0, len(cfg.FileExtensions))
	for dir := range cfg.FileExtensions {
		topLevelDirs = append(topLevelDirs, dir)
	}

	dirName := topLevelDirs[rand.IntN(len(topLevelDirs))]
	targetDir := filepath.Join(cfg.BaseDir, dirName)

	ext := getExtensionForCategory(dirName)
	fileName := fmt.Sprintf("new_f_%d_%s%s", time.Now().UnixNano(), dirName, ext)
	filePath := filepath.Join(targetDir, fileName)

	fileSize := rand.IntN(9216) + 1024

	fr := NewFastRandom(64 * 1024)
	if err := writeRandomDataFast(filePath, fileSize, fr); err != nil {
		return fmt.Errorf("failed to write new file %s: %w", filePath, err)
	}

	if err := setMetadata(filePath, false); err != nil {
		fmt.Printf("Warning: Failed to set metadata on new file: %v\n", err)
	}

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

			for _, action := range actions {
				cumulativeWeight += action.weight
				if r < cumulativeWeight {
					chosenAction = action.f
					break
				}
			}

			if chosenAction != nil {
				if err := chosenAction(idx); err != nil {
					if !os.IsNotExist(err) && !strings.Contains(err.Error(), "no such file") {
						fmt.Printf("Action failed: %v\n", err)
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
	case "update":
		if err := runDynamicUpdate(); err != nil {
			fmt.Printf("FATAL UPDATE ERROR: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Invalid mode: %s. Use 'populate', 'torture', or 'update'.\n", mode)
		os.Exit(1)
	}
}
