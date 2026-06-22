package exportx

import (
	"os"

	"novera/internal/bigfile/fileio"
)

var removeFile = os.Remove

type createdOutput = fileio.ExclusiveOutput

func openCreatedOutput(path string) (*createdOutput, error) {
	return fileio.OpenExclusiveOutput(path, 0o600)
}
