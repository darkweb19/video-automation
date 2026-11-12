package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const validEmptyDataset = `{"version":"framevault.clipping.gemini.evaluation.v1","corpus":[],"runs":[]}`

func TestDecodeDatasetAcceptsVersionedEmptyContract(t *testing.T) {
	dataset, err := decodeDataset(strings.NewReader(validEmptyDataset))
	if err != nil {
		t.Fatalf("decodeDataset() error = %v", err)
	}
	if dataset.Version != evaluationInputVersion || dataset.Corpus == nil || dataset.Runs == nil {
		t.Fatalf("decoded dataset = %#v", dataset)
	}
}

func TestDecodeDatasetRejectsInvalidOrAmbiguousInput(t *testing.T) {
	validWithDuplicateVersion := `{"version":"framevault.clipping.gemini.evaluation.v1","version":"framevault.clipping.gemini.evaluation.v1","corpus":[],"runs":[]}`
	unknownField := `{"version":"framevault.clipping.gemini.evaluation.v1","corpus":[],"runs":[],"url":"https://example.invalid/video"}`
	duplicateEscapedKey := `{"version":"framevault.clipping.gemini.evaluation.v1","corpus":[],"runs":[],"\u0075rl":"secret"}`
	wrongVersion := `{"version":"evaluation.v99","corpus":[],"runs":[]}`
	missingArrays := `{"version":"framevault.clipping.gemini.evaluation.v1"}`
	nullArrays := `{"version":"framevault.clipping.gemini.evaluation.v1","corpus":null,"runs":[]}`
	secondDocument := validEmptyDataset + ` {"version":"framevault.clipping.gemini.evaluation.v1","corpus":[],"runs":[]}`
	badSurrogate := strings.Replace(validEmptyDataset, `"runs"`, `"bad":"\uD800","runs"`, 1)
	invalidUTF8 := append([]byte(validEmptyDataset), 0xff)
	tooLarge := append([]byte(validEmptyDataset), bytes.Repeat([]byte{' '}, maxEvaluationInputBytes-len(validEmptyDataset)+1)...)

	cases := map[string][]byte{
		"nil reader":                 nil,
		"duplicate field":            []byte(validWithDuplicateVersion),
		"unknown field":              []byte(unknownField),
		"duplicate escaped field":    []byte(duplicateEscapedKey),
		"wrong version":              []byte(wrongVersion),
		"missing required arrays":    []byte(missingArrays),
		"null required array":        []byte(nullArrays),
		"trailing document":          []byte(secondDocument),
		"unpaired escaped surrogate": []byte(badSurrogate),
		"invalid raw UTF-8":          invalidUTF8,
		"oversized input":            tooLarge,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var reader io.Reader
			if input != nil {
				reader = bytes.NewReader(input)
			}
			_, err := decodeDataset(reader)
			if !errors.Is(err, errInvalidDataset) {
				t.Fatalf("decodeDataset() error = %v, want %v", err, errInvalidDataset)
			}
		})
	}
}

func TestDecodeDatasetHonorsByteAndNestingLimits(t *testing.T) {
	// Whitespace is legal after the one JSON value, so an exactly-at-limit
	// document proves the byte ceiling is inclusive without creating a large
	// semantic dataset.
	atLimit := append([]byte(validEmptyDataset), bytes.Repeat([]byte{' '}, maxEvaluationInputBytes-len(validEmptyDataset))...)
	if _, err := decodeDataset(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("decode exactly at the input limit: %v", err)
	}

	tooDeep := []byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66))
	if err := validateUniqueJSON(tooDeep); err == nil {
		t.Fatal("validateUniqueJSON accepted excessive nesting")
	}
}

func TestValidEvaluationUnicodeEscapesPreservesSupplementaryCharacters(t *testing.T) {
	if !validEvaluationUnicodeEscapes([]byte(`{"label":"\uD83D\uDE80"}`)) {
		t.Fatal("valid surrogate pair was rejected")
	}
	if validEvaluationUnicodeEscapes([]byte(`{"label":"\uDE80"}`)) {
		t.Fatal("unpaired low surrogate was accepted")
	}
}
