package config

// EnvNote describes an environment variable that is read but is not a
// configuration setting.
type EnvNote struct {
	Name        string
	Description string
}

// InternalEnv lists the MNEMOS_* variables the code reads that are not
// mnemos.yaml settings: process plumbing, diagnostics and test hooks. The
// configuration reference lists them separately, and
// TestEveryEnvNameTheCodeReadsIsDocumented fails on any name in neither list.
var InternalEnv = []EnvNote{
	{"MNEMOS_CONFIG", "Path to the mnemos.yaml to load, overriding discovery. The --config flag wins over it. It names the file, so it cannot be set in the file."},
	{"MNEMOS_RELATE_TRACE", "Diagnostics: when true, every incremental relate pass writes one line to stderr (candidates, pairs evaluated, edges kept, tokens the candidate budget skipped)."},
}
