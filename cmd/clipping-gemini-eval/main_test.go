package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWritesRedactedJSONFromExplicitFileOrStdin(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.Runs[0].Analysis = bytes.ReplaceAll(dataset.Runs[0].Analysis, []byte("PRIVATE_MODEL_HOOK_ONE"), []byte("SECRET_MODEL_TEXT https://private.invalid/watch?v=key-xyz"))
	payload, err := json.Marshal(dataset)
	if err != nil {
		t.Fatalf("marshal input dataset: %v", err)
	}

	path := filepath.Join(t.TempDir(), "evaluation.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	for name, args := range map[string][]string{
		"file":  {"--input", path},
		"stdin": {"--input", "-"},
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			if err := run(args, bytes.NewReader(payload), &output); err != nil {
				t.Fatalf("run(): %v", err)
			}
			var report EvaluationReport
			if err := json.Unmarshal(output.Bytes(), &report); err != nil {
				t.Fatalf("output is not JSON: %v", err)
			}
			if report.Version != evaluationReportVersion || len(report.Runs) != 1 {
				t.Fatalf("report = %#v", report)
			}
			for _, secret := range []string{"SECRET_MODEL_TEXT", "private.invalid", "key-xyz", "PRIVATE_PROMPT_VERSION", "snapshot-model-private"} {
				if strings.Contains(output.String(), secret) {
					t.Errorf("report echoed private input marker %q", secret)
				}
			}
		})
	}
}

func TestRunRejectsCLIAndInputErrorsWithoutPartialOutput(t *testing.T) {
	for name, args := range map[string][]string{
		"missing input":         {},
		"empty input":           {"--input="},
		"extra positional arg":  {"--input", "-", "private-path"},
		"unknown endpoint flag": {"--input", "-", "--endpoint", "https://secret.invalid"},
		"unreadable file":       {"--input", filepath.Join(t.TempDir(), "missing-secret-file.json")},
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			err := run(args, strings.NewReader("secret-body"), &output)
			if !errors.Is(err, errEvaluationCLI) {
				t.Fatalf("run() error = %v, want fixed CLI error", err)
			}
			if output.Len() != 0 {
				t.Fatalf("failure wrote partial output: %q", output.String())
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "https://") {
				t.Fatalf("error leaked an input or argument: %q", err)
			}
		})
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("private output failure") }

func TestRunRejectsMissingStdinAndHidesOutputWriterErrors(t *testing.T) {
	if err := run([]string{"--input", "-"}, nil, &bytes.Buffer{}); !errors.Is(err, errEvaluationCLI) {
		t.Fatalf("nil stdin error = %v", err)
	}
	if err := run([]string{"--input", "-"}, strings.NewReader(validEmptyDataset), failedWriter{}); !errors.Is(err, errEvaluationCLI) {
		t.Fatalf("writer error = %v", err)
	}
}
