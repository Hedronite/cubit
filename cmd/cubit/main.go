// Command cubit measures Cargo disk usage on build hosts and exports it as
// Prometheus metrics. It measures; it never cleans.
package main

import (
	"fmt"
	"os"
)

func main() {
	raiseFileLimit()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process: it returns the exit code. A panic is
// recovered into code 6, so a crash never reads as a scan result.
func run(args []string, stdout, stderr interface {
	Write([]byte) (int, error)
}) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "cubit: internal error: %v\n", r)
			code = exitInternal
		}
	}()
	return execute(args, stdout, stderr)
}
