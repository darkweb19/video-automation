package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

var errEvaluationCLI = errors.New("clipping Gemini evaluation failed")

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, errEvaluationCLI)
		os.Exit(2)
	}
}

// run accepts one explicitly selected local JSON file, or "-" for stdin, and
// writes its safe report to stdout. It has no provider, endpoint, credential,
// media path, download, or output-file option. Every failure is mapped to one
// fixed message by main.
func run(args []string, stdin io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("clipping-gemini-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "local versioned evaluation JSON, or - for stdin")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *inputPath == "" || output == nil {
		return errEvaluationCLI
	}
	var input io.Reader
	if *inputPath == "-" {
		input = stdin
	} else {
		file, err := os.Open(*inputPath)
		if err != nil {
			return errEvaluationCLI
		}
		defer file.Close()
		input = file
	}
	if input == nil {
		return errEvaluationCLI
	}
	dataset, err := decodeDataset(input)
	if err != nil {
		return errEvaluationCLI
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(evaluateDataset(dataset)); err != nil {
		return errEvaluationCLI
	}
	return nil
}
