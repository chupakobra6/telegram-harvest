package transcribe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const DefaultFFmpegCommand = "ffmpeg"

type Options struct {
	CommandTemplate string
	VoskCommand     string
	VoskModelPath   string
	VoskGrammarPath string
	FFmpegCommand   string
}

func (o Options) Configured() bool {
	if strings.TrimSpace(o.CommandTemplate) != "" {
		return true
	}
	return strings.TrimSpace(o.VoskCommand) != "" && strings.TrimSpace(o.VoskModelPath) != ""
}

func (o Options) EngineName() string {
	if strings.TrimSpace(o.CommandTemplate) != "" {
		return "command"
	}
	if strings.TrimSpace(o.VoskCommand) != "" || strings.TrimSpace(o.VoskModelPath) != "" {
		return "vosk"
	}
	return ""
}

func Run(ctx context.Context, opts Options, inputPath string, outputPath string) (string, error) {
	if strings.TrimSpace(inputPath) == "" {
		return "", fmt.Errorf("input path is empty")
	}
	if strings.TrimSpace(outputPath) == "" {
		return "", fmt.Errorf("output path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return "", fmt.Errorf("prepare transcript dir: %w", err)
	}
	if strings.TrimSpace(opts.CommandTemplate) != "" {
		return runCommandTemplate(ctx, opts.CommandTemplate, inputPath, outputPath)
	}
	return runVosk(ctx, opts, inputPath, outputPath)
}

func runVosk(ctx context.Context, opts Options, inputPath string, outputPath string) (string, error) {
	voskCommand := strings.TrimSpace(opts.VoskCommand)
	if voskCommand == "" {
		return "", fmt.Errorf("vosk command is empty")
	}
	modelPath := strings.TrimSpace(opts.VoskModelPath)
	if modelPath == "" {
		return "", fmt.Errorf("vosk model path is empty")
	}
	ffmpegCommand := strings.TrimSpace(opts.FFmpegCommand)
	if ffmpegCommand == "" {
		ffmpegCommand = DefaultFFmpegCommand
	}

	wavFile, err := os.CreateTemp(filepath.Dir(outputPath), ".vosk-*.wav")
	if err != nil {
		return "", fmt.Errorf("create temporary wav: %w", err)
	}
	wavPath := wavFile.Name()
	if err := wavFile.Close(); err != nil {
		_ = os.Remove(wavPath)
		return "", fmt.Errorf("close temporary wav: %w", err)
	}
	defer os.Remove(wavPath)

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", inputPath,
		"-ac", "1",
		"-ar", "16000",
		"-sample_fmt", "s16",
		wavPath,
	}
	if output, err := exec.CommandContext(ctx, ffmpegCommand, args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(output)))
	}

	voskArgs := []string{modelPath, wavPath}
	if grammarPath := strings.TrimSpace(opts.VoskGrammarPath); grammarPath != "" {
		voskArgs = append(voskArgs, grammarPath)
	}
	cmd := exec.CommandContext(ctx, voskCommand, voskArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("vosk: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	text := strings.TrimSpace(stdout.String())
	if err := os.WriteFile(outputPath, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("write transcript: %w", err)
	}
	return text, nil
}

func runCommandTemplate(ctx context.Context, template string, inputPath string, outputPath string) (string, error) {
	outputDir := filepath.Dir(outputPath)
	outputBase := strings.TrimSuffix(outputPath, filepath.Ext(outputPath))
	command := strings.ReplaceAll(template, "{input}", shellQuote(inputPath))
	command = strings.ReplaceAll(command, "{output}", shellQuote(outputPath))
	command = strings.ReplaceAll(command, "{output_dir}", shellQuote(outputDir))
	command = strings.ReplaceAll(command, "{output_base}", shellQuote(outputBase))
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, detail)
	}
	text := strings.TrimSpace(string(output))
	if _, statErr := os.Stat(outputPath); os.IsNotExist(statErr) {
		if err := os.WriteFile(outputPath, []byte(text), 0o600); err != nil {
			return "", fmt.Errorf("write transcript: %w", err)
		}
	}
	return text, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
