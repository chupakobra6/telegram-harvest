package harvest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DefaultDailyRetentionDays = 0

type DailyRetentionOptions struct {
	StateDir   string
	RetainDays int
	Now        time.Time
}

type DailyRetentionStats struct {
	DeletedFiles int
	DeletedDirs  int
}

func PruneDailyState(opts DailyRetentionOptions) (DailyRetentionStats, error) {
	if strings.TrimSpace(opts.StateDir) == "" {
		return DailyRetentionStats{}, fmt.Errorf("state dir is required")
	}
	if opts.RetainDays <= 0 {
		return DailyRetentionStats{}, nil
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	cutoffDay := localDay(now).AddDate(0, 0, -(opts.RetainDays - 1))
	cutoffInstant := cutoffDay

	var stats DailyRetentionStats
	if err := pruneDailyDayFiles(filepath.Join(opts.StateDir, "days"), cutoffDay, &stats); err != nil {
		return stats, err
	}
	if err := pruneDailyDayFiles(filepath.Join(opts.StateDir, "jsonl"), cutoffDay, &stats); err != nil {
		return stats, err
	}
	if err := pruneDailyDayFiles(DailyDefaultReportRoot(opts.StateDir), cutoffDay, &stats); err != nil {
		return stats, err
	}
	if err := pruneDailyDayFiles(filepath.Join(opts.StateDir, "reports", "jsonl"), cutoffDay, &stats); err != nil {
		return stats, err
	}
	if err := pruneDailyDayFiles(filepath.Join(opts.StateDir, "reports", "md"), cutoffDay, &stats); err != nil {
		return stats, err
	}
	if err := pruneDateLeafDirs(filepath.Join(opts.StateDir, "media"), cutoffDay, &stats); err != nil {
		return stats, err
	}
	if err := pruneDateLeafDirs(filepath.Join(opts.StateDir, "transcripts"), cutoffDay, &stats); err != nil {
		return stats, err
	}
	if err := pruneOldFilesByMTime(filepath.Join(opts.StateDir, "transcripts", "cache"), cutoffInstant, &stats); err != nil {
		return stats, err
	}
	if err := pruneEmptyDirs(filepath.Join(opts.StateDir, "media"), &stats); err != nil {
		return stats, err
	}
	if err := pruneEmptyDirs(filepath.Join(opts.StateDir, "transcripts"), &stats); err != nil {
		return stats, err
	}
	if err := pruneEmptyDirs(filepath.Join(opts.StateDir, "reports"), &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

func localDay(value time.Time) time.Time {
	local := value.In(moscowLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, moscowLocation)
}

func pruneDailyDayFiles(root string, cutoffDay time.Time, stats *DailyRetentionStats) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		day, ok := parseDatePrefix(entry.Name())
		if !ok || !day.Before(cutoffDay) {
			continue
		}
		if err := os.Remove(filepath.Join(root, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
		stats.DeletedFiles++
	}
	return nil
}

func pruneDateLeafDirs(root string, cutoffDay time.Time, stats *DailyRetentionStats) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() || path == root {
			return nil
		}
		day, ok := parseDatePrefix(filepath.Base(path))
		if !ok || !day.Before(cutoffDay) {
			return nil
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		stats.DeletedDirs++
		return filepath.SkipDir
	})
}

func pruneOldFilesByMTime(root string, cutoff time.Time, stats *DailyRetentionStats) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		stats.DeletedFiles++
		return nil
	})
}

func pruneEmptyDirs(root string, stats *DailyRetentionStats) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			continue
		}
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			return err
		}
		stats.DeletedDirs++
	}
	return nil
}

func parseDatePrefix(value string) (time.Time, bool) {
	if len(value) < len("2006-01-02") {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("2006-01-02", value[:len("2006-01-02")], moscowLocation)
	return day, err == nil
}
