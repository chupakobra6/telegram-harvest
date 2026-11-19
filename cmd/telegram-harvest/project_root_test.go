package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
)

func TestBuiltCLIResolvesProjectFromAnotherDirectory(t *testing.T) {
	temp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(temp, "harvest")
	foreign := filepath.Join(temp, "foreign")
	installDir := filepath.Join(temp, "local-bin")
	binary := filepath.Join(root, "bin", "telegram-harvest")
	link := filepath.Join(installDir, "telegram-harvest")
	mustWriteCLIFile(t, filepath.Join(root, "go.mod"), "module github.com/chupakobra6/telegram-harvest\n")
	mustWriteCLIFile(t, filepath.Join(root, ".env"), "TG_HARVEST_STUDY_APP_ID=123\n"+
		"TG_HARVEST_STUDY_SESSION_PATH=sessions/study.json\n"+
		"TG_HARVEST_STUDY_STATE_DIR=project-state\n"+
		"TG_HARVEST_DAILY_FFMPEG_COMMAND=ffmpeg\n"+
		"TG_HARVEST_DAILY_WHISPER_COMMAND=runtime/whisper-server\n"+
		"TG_HARVEST_DAILY_WHISPER_MODEL_PATH=runtime/"+transcribe.ProductionModelFile+"\n"+
		"TG_HARVEST_DAILY_WHISPER_SPEECH_GATE_MODEL_PATH=runtime/"+transcribe.ProductionSpeechGateFile+"\n")
	mustWriteCLIFile(t, filepath.Join(foreign, ".env"), "TG_HARVEST_STUDY_STATE_DIR=foreign-state\n")
	mustWriteCLIFile(t, filepath.Join(root, "project-state", "messages.jsonl"),
		"{\"chat\":{\"id\":1},\"message_id\":1,\"kind\":\"text\",\"text\":\"fixture\"}\n")
	mustWriteCLIFile(t, filepath.Join(root, "runtime", transcribe.ProductionModelFile), "synthetic model")
	mustWriteCLIFile(t, filepath.Join(root, "runtime", transcribe.ProductionSpeechGateFile), "synthetic gate")
	for _, path := range []string{
		filepath.Join(installDir, "ffmpeg"),
		filepath.Join(root, "runtime", "whisper-server"),
		filepath.Join(root, "runtime", "whisper-vad-speech-segments"),
	} {
		mustWriteCLIFile(t, path, "#!/bin/sh\nprintf 'fixture process invoked\\n' >&2\nexit 1\n")
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	build := exec.Command("go", "build", "-trimpath", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, command *exec.Cmd, values ...string) string {
		t.Helper()
		command.Dir = foreign
		command.Env = append([]string{"PATH=" + installDir, "HOME=" + temp}, values...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("run CLI from %s: %v\n%s", foreign, err, output)
		}
		return string(output)
	}
	assertOutput := func(t *testing.T, output string, lines ...string) {
		t.Helper()
		for _, line := range lines {
			if !strings.Contains(output, line+"\n") {
				t.Errorf("expected %q in output:\n%s", line, output)
			}
		}
	}
	session := filepath.Join(root, "sessions", "study.json")
	configArgs := []string{"--profile", "study", "print-config"}
	t.Run("direct binary", func(t *testing.T) {
		assertOutput(t, run(t, exec.Command(binary, configArgs...)),
			"app_id_set=true", "session="+session, "runtime_lock="+session+".runtime.lock",
			"state_dir="+filepath.Join(root, "project-state"))
	})
	t.Run("PATH symlink and environment override", func(t *testing.T) {
		output := run(t, exec.Command("/bin/sh", "-c", "exec telegram-harvest \"$@\"", "sh",
			"--profile", "study", "print-config"), "TG_HARVEST_STUDY_STATE_DIR=explicit-state")
		assertOutput(t, output, "session="+session, "runtime_lock="+session+".runtime.lock",
			"state_dir="+filepath.Join(root, "explicit-state"))
	})
	t.Run("relative view paths use the selected profile state", func(t *testing.T) {
		run(t, exec.Command(link, "--profile", "study", "compact", "--in", "messages.jsonl", "--out", "messages.toon"))
		if output := readCLIFile(t, filepath.Join(root, "project-state", "messages.toon")); !strings.Contains(output, "fixture") {
			t.Fatalf("expected fixture message in compact output: %s", output)
		}
	})
	t.Run("relative ASR configuration uses the dotenv directory", func(t *testing.T) {
		output := run(t, exec.Command(link, "--profile", "main", "transcribe-file", "--check"))
		if !strings.Contains(output, transcribeValidationRuntimeReady) {
			t.Fatalf("ASR runtime is not ready: %s", output)
		}
		output = run(t, exec.Command(link, "--profile", "main", "print-config"),
			"TG_HARVEST_DAILY_WHISPER_COMMAND=caller/whisper-server")
		assertOutput(t, output, "daily_whisper_command=caller/whisper-server")
	})
	t.Run("relative media input uses the caller directory", func(t *testing.T) {
		mustWriteCLIFile(t, filepath.Join(foreign, "media.wav"), "synthetic media")
		command := exec.Command(link, "--profile", "main", "transcribe-file", "--input", "media.wav", "--output", "transcript.txt")
		command.Dir = foreign
		command.Env = []string{"PATH=" + installDir, "HOME=" + temp}
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "fixture process invoked") {
			t.Fatalf("expected ffmpeg to open the caller's input: %v\n%s", err, output)
		}
	})
	t.Run("standalone binary uses current directory", func(t *testing.T) {
		standaloneDir := filepath.Join(temp, "other-tool", "bin")
		standalone := filepath.Join(standaloneDir, "telegram-harvest")
		mustWriteCLIFile(t, filepath.Join(standaloneDir, "..", "go.mod"), "module example.com/other-tool\n")
		if err := os.Mkdir(standaloneDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(binary, standalone); err != nil {
			t.Fatal(err)
		}
		output := run(t, exec.Command(standalone, configArgs...))
		assertOutput(t, output, "app_id_set=false", "session="+filepath.Join(foreign, ".sessions", "study.json"),
			"state_dir="+filepath.Join(foreign, "foreign-state"))
	})
}
