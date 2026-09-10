// Command tram is a viewer and safe editor for ssh_config, plus a handful of
// commands that run across many hosts at once.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/hieuny/tram/cmd"
	"github.com/hieuny/tram/internal/secret"
	"github.com/hieuny/tram/internal/store"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	cmd.Version = version

	// ssh re-invokes tram as its askpass helper. That invocation is not a
	// command line the user typed, so it is handled before cobra sees it: the
	// helper writes one secret to standard output and exits, and answers
	// nothing else.
	if secret.IsAskpassInvocation(os.Args[1:]) {
		os.Exit(runAskpass())
	}

	root := cmd.Root()
	if err := root.Execute(); err != nil {
		var ec cmd.ExitCode
		if errors.As(err, &ec) {
			if ec.Err != nil {
				fmt.Fprintln(os.Stderr, "error:", ec.Err)
			}
			os.Exit(ec.Code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runAskpass answers one question from ssh.
//
// It answers a password or a key passphrase and nothing else. A host key
// confirmation is refused on purpose: answering "yes" to an unrecognised
// fingerprint on the user's behalf would turn a warning about a possible
// interception into a silent accept.
func runAskpass() int {
	prompt := ""
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}
	st := secret.New(store.Dir())
	if err := secret.Askpass(st, prompt, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tram askpass:", err)
		return 1
	}
	return 0
}
