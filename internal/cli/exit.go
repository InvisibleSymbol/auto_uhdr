// Package cli implements the arw2uhdr command-line front end. Each command is a
// Run* function returning an error; an *ExitError carries the process exit code.
// Keeping the logic here (rather than in package main) makes it unit-testable.
package cli

import (
	"errors"
	"fmt"

	"github.com/invis/arw2uhdr"
)

// ExitCode is the process exit status. The values are a stable scripting contract.
type ExitCode int

const (
	ExitOK     ExitCode = 0
	ExitUsage  ExitCode = 2
	ExitInput  ExitCode = 3
	ExitDecode ExitCode = 4
	ExitLens   ExitCode = 5
	ExitRender ExitCode = 6
	ExitEncode ExitCode = 7
	ExitWrite  ExitCode = 8
)

// ExitError pairs an exit code with an optional message for stderr.
type ExitError struct {
	Code    ExitCode
	Message string
}

func (e *ExitError) Error() string { return e.Message }

func usageErr(format string, a ...any) *ExitError {
	return &ExitError{Code: ExitUsage, Message: fmt.Sprintf(format, a...)}
}

func inputErr(format string, a ...any) *ExitError {
	return &ExitError{Code: ExitInput, Message: fmt.Sprintf(format, a...)}
}

// convertExit maps a pipeline error to the exit code for its stage.
func convertExit(err error) *ExitError {
	code := ExitEncode
	var se *arw2uhdr.StageError
	if errors.As(err, &se) {
		switch se.Stage {
		case arw2uhdr.StageDecode:
			code = ExitDecode
		case arw2uhdr.StageLens:
			code = ExitLens
		case arw2uhdr.StageInput:
			code = ExitInput
		case arw2uhdr.StageRender:
			code = ExitRender
		case arw2uhdr.StageEncode:
			code = ExitEncode
		case arw2uhdr.StageWrite:
			code = ExitWrite
		}
	}
	return &ExitError{Code: code, Message: err.Error()}
}
