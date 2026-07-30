package harvest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const DailyLatestCatchupFilename = "00-latest-catchup.md"

type DailyCatchupMarkdownOptions struct {
	OutputPath string
	ReportDir  string
	Dates      []string
}

func WriteDailyCatchupMarkdown(opts DailyCatchupMarkdownOptions) error {
	if strings.TrimSpace(opts.OutputPath) == "" {
		return fmt.Errorf("output path is required")
	}
	if strings.TrimSpace(opts.ReportDir) == "" {
		return fmt.Errorf("report directory is required")
	}
	dates := sortedUniqueDates(opts.Dates)
	if len(dates) == 0 {
		return fmt.Errorf("at least one daily report date is required")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Telegram catch-up: %s — %s\n\n", dates[0], dates[len(dates)-1])
	fmt.Fprintf(&b, "Дней: %d. Источник: ежедневные отчёты Telegram Harvest.\n\n", len(dates))

	for index, date := range dates {
		content, err := os.ReadFile(filepath.Join(opts.ReportDir, date+".md"))
		if err != nil {
			return fmt.Errorf("read daily report %s: %w", date, err)
		}
		if index > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "---\n\n## %s\n\n", date)
		mergedDay := demoteDailyMarkdownHeadings(string(content))
		b.WriteString(mergedDay)
		if !strings.HasSuffix(mergedDay, "\n") {
			b.WriteString("\n")
		}
	}
	return writeTextFile(opts.OutputPath, b.String())
}

func sortedUniqueDates(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		date := strings.TrimSpace(value)
		if date == "" {
			continue
		}
		if _, ok := seen[date]; ok {
			continue
		}
		seen[date] = struct{}{}
		result = append(result, date)
	}
	sort.Strings(result)
	return result
}

func demoteDailyMarkdownHeadings(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var b strings.Builder
	removedTitle := false
	for _, line := range lines {
		if !removedTitle && strings.HasPrefix(line, "# ") {
			removedTitle = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			line = "#" + line
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimLeft(b.String(), "\n")
}
