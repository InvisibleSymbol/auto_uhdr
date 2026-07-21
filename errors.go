package arw2uhdr

// Stage identifies a pipeline step, for error attribution and CLI exit codes.
type Stage string

const (
	StageDecode Stage = "decode" // raw develop
	StageLens   Stage = "lens"   // embedded lens correction / metadata
	StageInput  Stage = "input"  // reading/loading the SDR base JPEG
	StageRender Stage = "render" // HDR rendition
	StageEncode Stage = "encode" // gain map + Ultra HDR container
	StageWrite  Stage = "write"  // writing the output file
)

// StageError wraps an error with the pipeline stage that produced it. Callers can
// use errors.As to recover the Stage (the CLI maps it to an exit code).
type StageError struct {
	Stage Stage
	Err   error
}

func (e *StageError) Error() string { return string(e.Stage) + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// stageErr wraps err in a StageError, or returns nil if err is nil.
func stageErr(s Stage, err error) error {
	if err == nil {
		return nil
	}
	return &StageError{Stage: s, Err: err}
}
