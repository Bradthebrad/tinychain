package mcp

import (
	"io"
	"os"
)

func defaultStdin() io.Reader {
	return os.Stdin
}

func defaultStdout() io.Writer {
	return os.Stdout
}
