// Command tram is a viewer and safe editor for ssh_config, plus a handful of
// commands that run across many hosts at once.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/hieuny/tram/cmd"
	"github.com/hieuny/tram/internal/secret"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	cmd.Version = version

	// ssh re-invokes tram as its askpass helper. That invocation is not a
	// command line the user typed, so it is handled before cobra sees it: the
	// helper writes one answer to standard output and exits.
	if secret.IsAskpassInvocation(os.Args[1:]) {
		os.Exit(runAskpass())
	}
	os.Exit(run())
}

// run holds the whole of a normal invocation so that the cleanup can be
// deferred. os.Exit does not run deferred functions, so calling it from main
// directly would leave this run's passphrase cache behind on every exit,
// including the successful ones.
func run() int {
	defer cmd.CloseSession()

	if err := cmd.Root().Execute(); err != nil {
		var ec cmd.ExitCode
		if errors.As(err, &ec) {
			if ec.Err != nil {
				fmt.Fprintln(os.Stderr, "error:", ec.Err)
			}
			return ec.Code
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// runAskpass answers one question from ssh.
//
// It answers a key passphrase, reusing this run's if one was already typed, and
// a password, which it forwards without keeping. A host key confirmation is
// refused on purpose: answering "yes" to an unrecognised fingerprint on the
// user's behalf would turn a warning about a possible interception into a
// silent accept.
func runAskpass() int {
	prompt := ""
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}
	if err := secret.Askpass(prompt, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tram askpass:", err)
		return 1
	}
	return 0
}
