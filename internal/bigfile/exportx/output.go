package exportx

import "novera/internal/bigfile/fileio"

type createdOutput = fileio.AtomicOutput

func openCreatedOutput(path string, sourcePaths ...string) (*createdOutput, error) {
	return fileio.OpenAtomicOutput(path, sourcePaths, 0o600)
}
