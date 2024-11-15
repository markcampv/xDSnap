// cmd/root.go
package main

import (
	"os" 
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"github.com/markcampv/xDSnap/pkg/cmd"

)

func main() {
	flags := pflag.NewFlagSet("kubectl-xdsnap", pflag.ExitOnError)
	pflag.CommandLine = flags

	root := cmd.NewRootCommand(genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
