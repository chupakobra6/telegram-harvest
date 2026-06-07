//go:build cgo

package main

/*
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

typedef struct VoskModel VoskModel;
typedef struct VoskRecognizer VoskRecognizer;

typedef void (*vosk_set_log_level_fn)(int);
typedef VoskModel* (*vosk_model_new_fn)(const char*);
typedef void (*vosk_model_free_fn)(VoskModel*);
typedef VoskRecognizer* (*vosk_recognizer_new_fn)(VoskModel*, float);
typedef VoskRecognizer* (*vosk_recognizer_new_grm_fn)(VoskModel*, float, const char*);
typedef void (*vosk_recognizer_free_fn)(VoskRecognizer*);
typedef int (*vosk_recognizer_accept_waveform_fn)(VoskRecognizer*, const char*, int);
typedef const char* (*vosk_recognizer_result_fn)(VoskRecognizer*);
typedef const char* (*vosk_recognizer_final_result_fn)(VoskRecognizer*);

typedef struct {
	void *handle;
	vosk_set_log_level_fn set_log_level;
	vosk_model_new_fn model_new;
	vosk_model_free_fn model_free;
	vosk_recognizer_new_fn recognizer_new;
	vosk_recognizer_new_grm_fn recognizer_new_grm;
	vosk_recognizer_free_fn recognizer_free;
	vosk_recognizer_accept_waveform_fn recognizer_accept_waveform;
	vosk_recognizer_result_fn recognizer_result;
	vosk_recognizer_final_result_fn recognizer_final_result;
} vosk_api;

static int load_symbol(void *handle, const char *name, void **target) {
	*target = dlsym(handle, name);
	return *target != NULL;
}

static char* copy_error(const char *message) {
	if (message == NULL) {
		return NULL;
	}
	return strdup(message);
}

static int load_vosk_api(const char *path, vosk_api *api, char **err) {
	api->handle = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (api->handle == NULL) {
		*err = copy_error(dlerror());
		return 0;
	}
	if (!load_symbol(api->handle, "vosk_set_log_level", (void**)&api->set_log_level) ||
		!load_symbol(api->handle, "vosk_model_new", (void**)&api->model_new) ||
		!load_symbol(api->handle, "vosk_model_free", (void**)&api->model_free) ||
		!load_symbol(api->handle, "vosk_recognizer_new", (void**)&api->recognizer_new) ||
		!load_symbol(api->handle, "vosk_recognizer_new_grm", (void**)&api->recognizer_new_grm) ||
		!load_symbol(api->handle, "vosk_recognizer_free", (void**)&api->recognizer_free) ||
		!load_symbol(api->handle, "vosk_recognizer_accept_waveform", (void**)&api->recognizer_accept_waveform) ||
		!load_symbol(api->handle, "vosk_recognizer_result", (void**)&api->recognizer_result) ||
		!load_symbol(api->handle, "vosk_recognizer_final_result", (void**)&api->recognizer_final_result)) {
		*err = copy_error(dlerror());
		if (*err == NULL) {
			*err = copy_error("missing Vosk API symbol");
		}
		dlclose(api->handle);
		api->handle = NULL;
		return 0;
	}
	return 1;
}

static void close_vosk_api(vosk_api *api) {
	if (api->handle != NULL) {
		dlclose(api->handle);
		api->handle = NULL;
	}
}

static void vosk_set_log_level_call(vosk_api *api, int level) {
	api->set_log_level(level);
}

static VoskModel* vosk_model_new_call(vosk_api *api, const char *model_dir) {
	return api->model_new(model_dir);
}

static void vosk_model_free_call(vosk_api *api, VoskModel *model) {
	api->model_free(model);
}

static VoskRecognizer* vosk_recognizer_new_call(vosk_api *api, VoskModel *model, float sample_rate) {
	return api->recognizer_new(model, sample_rate);
}

static VoskRecognizer* vosk_recognizer_new_grm_call(vosk_api *api, VoskModel *model, float sample_rate, const char *grammar) {
	return api->recognizer_new_grm(model, sample_rate, grammar);
}

static void vosk_recognizer_free_call(vosk_api *api, VoskRecognizer *recognizer) {
	api->recognizer_free(recognizer);
}

static int vosk_recognizer_accept_waveform_call(vosk_api *api, VoskRecognizer *recognizer, const char *data, int len) {
	return api->recognizer_accept_waveform(recognizer, data, len);
}

static const char* vosk_recognizer_result_call(vosk_api *api, VoskRecognizer *recognizer) {
	return api->recognizer_result(recognizer);
}

static const char* vosk_recognizer_final_result_call(vosk_api *api, VoskRecognizer *recognizer) {
	return api->recognizer_final_result(recognizer);
}
*/
import "C"

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"
)

const voskSessionFlag = "--session"

type recognitionResult struct {
	Text string `json:"text"`
}

type sessionRequest struct {
	ID      int    `json:"id"`
	WAVPath string `json:"wav_path"`
}

type sessionResponse struct {
	ID    int    `json:"id"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

type voskAPI struct {
	api C.vosk_api
}

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) == 3 || len(os.Args) == 4 {
		if os.Args[1] == voskSessionFlag {
			grammarPath := ""
			if len(os.Args) == 4 {
				grammarPath = os.Args[3]
			}
			if err := runSession(os.Args[2], grammarPath); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return 0
		}
	}
	if len(os.Args) != 3 && len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: vosk-transcribe <model-dir> <wav-path> [grammar-json-path]")
		fmt.Fprintln(os.Stderr, "   or: vosk-transcribe --session <model-dir> [grammar-json-path]")
		return 2
	}
	grammarPath := ""
	if len(os.Args) == 4 {
		grammarPath = os.Args[3]
	}
	text, err := transcribe(os.Args[1], os.Args[2], grammarPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(text)
	return 0
}

func runSession(modelDir, grammarPath string) error {
	api, err := openVoskAPI()
	if err != nil {
		return err
	}
	defer api.Close()

	model, err := api.LoadModel(modelDir)
	if err != nil {
		return err
	}
	defer api.FreeModel(model)

	grammar, err := readGrammar(grammarPath)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request sessionRequest
		var response sessionResponse
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			response.Error = fmt.Sprintf("decode request: %v", err)
			if err := encoder.Encode(response); err != nil {
				return fmt.Errorf("write response: %w", err)
			}
			continue
		}
		response.ID = request.ID
		if strings.TrimSpace(request.WAVPath) == "" {
			response.Error = "wav_path is required"
			if err := encoder.Encode(response); err != nil {
				return fmt.Errorf("write response: %w", err)
			}
			continue
		}
		text, err := transcribeWithModel(api, model, request.WAVPath, grammar)
		if err != nil {
			response.Error = err.Error()
		} else {
			response.Text = text
		}
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}

func transcribe(modelDir, wavPath, grammarPath string) (string, error) {
	api, err := openVoskAPI()
	if err != nil {
		return "", err
	}
	defer api.Close()

	model, err := api.LoadModel(modelDir)
	if err != nil {
		return "", err
	}
	defer api.FreeModel(model)

	grammar, err := readGrammar(grammarPath)
	if err != nil {
		return "", err
	}
	return transcribeWithModel(api, model, wavPath, grammar)
}

func openVoskAPI() (*voskAPI, error) {
	var errors []string
	for _, path := range voskLibraryCandidates() {
		api, err := openVoskAPIPath(path)
		if err == nil {
			return api, nil
		}
		errors = append(errors, err.Error())
	}
	return nil, fmt.Errorf("load Vosk library: %s", strings.Join(errors, "; "))
}

func openVoskAPIPath(path string) (*voskAPI, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var cErr *C.char
	api := &voskAPI{}
	if C.load_vosk_api(cPath, &api.api, &cErr) == 0 {
		detail := "unknown error"
		if cErr != nil {
			detail = C.GoString(cErr)
			C.free(unsafe.Pointer(cErr))
		}
		return nil, fmt.Errorf("%s: %s", path, detail)
	}
	return api, nil
}

func (api *voskAPI) Close() {
	C.close_vosk_api(&api.api)
}

func (api *voskAPI) LoadModel(modelDir string) (*C.VoskModel, error) {
	C.vosk_set_log_level_call(&api.api, C.int(-1))
	cModelDir := C.CString(modelDir)
	defer C.free(unsafe.Pointer(cModelDir))
	model := C.vosk_model_new_call(&api.api, cModelDir)
	if model == nil {
		return nil, fmt.Errorf("failed to load vosk model: %s", modelDir)
	}
	return model, nil
}

func (api *voskAPI) FreeModel(model *C.VoskModel) {
	C.vosk_model_free_call(&api.api, model)
}

func voskLibraryCandidates() []string {
	seen := map[string]bool{}
	var candidates []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		candidates = append(candidates, path)
	}
	add(os.Getenv("TG_HARVEST_DAILY_VOSK_LIBRARY_PATH"))
	add(os.Getenv("VOSK_LIBRARY_PATH"))
	switch runtime.GOOS {
	case "darwin":
		add(filepath.Join("/opt/homebrew/lib", "libvosk.dylib"))
		add(filepath.Join("/usr/local/lib", "libvosk.dylib"))
		add("libvosk.dylib")
	default:
		add(filepath.Join("/usr/local/lib", "libvosk.so"))
		add(filepath.Join("/usr/lib", "libvosk.so"))
		add("libvosk.so")
	}
	return candidates
}

func readGrammar(grammarPath string) (string, error) {
	if strings.TrimSpace(grammarPath) == "" {
		return "", nil
	}
	grammarBytes, err := os.ReadFile(grammarPath)
	if err != nil {
		return "", fmt.Errorf("read grammar: %w", err)
	}
	grammar := strings.TrimSpace(string(grammarBytes))
	if grammar == "" {
		return "", fmt.Errorf("vosk grammar file is empty: %s", grammarPath)
	}
	return grammar, nil
}

func transcribeWithModel(api *voskAPI, model *C.VoskModel, wavPath string, grammar string) (string, error) {
	reader, err := openPCM16MonoWAV(wavPath)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	var recognizer *C.VoskRecognizer
	if strings.TrimSpace(grammar) != "" {
		cGrammar := C.CString(grammar)
		defer C.free(unsafe.Pointer(cGrammar))
		recognizer = C.vosk_recognizer_new_grm_call(&api.api, model, C.float(reader.SampleRate()), cGrammar)
	} else {
		recognizer = C.vosk_recognizer_new_call(&api.api, model, C.float(reader.SampleRate()))
	}
	if recognizer == nil {
		return "", errors.New("failed to create vosk recognizer")
	}
	defer C.vosk_recognizer_free_call(&api.api, recognizer)

	buf := make([]byte, 8000)
	parts := make([]string, 0, 8)
	for {
		n, err := reader.ReadChunk(buf)
		if n > 0 {
			accepted := C.vosk_recognizer_accept_waveform_call(
				&api.api,
				recognizer,
				(*C.char)(unsafe.Pointer(&buf[0])),
				C.int(n),
			)
			if accepted != 0 {
				parts = append(parts, extractTranscript(C.vosk_recognizer_result_call(&api.api, recognizer)))
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	parts = append(parts, extractTranscript(C.vosk_recognizer_final_result_call(&api.api, recognizer)))
	return joinTranscript(parts), nil
}

func extractTranscript(raw *C.char) string {
	payload := strings.TrimSpace(C.GoString(raw))
	if payload == "" {
		return ""
	}
	var result recognitionResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return ""
	}
	return strings.TrimSpace(result.Text)
}

func joinTranscript(parts []string) string {
	filtered := parts[:0]
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	return strings.Join(filtered, " ")
}
