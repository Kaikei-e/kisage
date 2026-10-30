package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/host"
	"github.com/Kaikei-e/kisage/mcp"
)

var version = "0.1.0"

func main() {
	configPath := os.Getenv("KISAGE_HOST_CONFIG")
	if configPath == "" {
		configPath = "/etc/kisage/host.json"
	}
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, configPath))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, configPath string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(stdout, "kisage plan|apply|prune FILE [-json]\nkisage explain FILE PATH [-json]\nkisage adopt|release FILE PATH --expect TOKEN --owner-revision REV [-json]\nkisage mcp\nkisage init-store\n\nplan also accepts -detailed-exitcode.\nHost policy: KISAGE_HOST_CONFIG (default /etc/kisage/host.json).\ninit-store is an administrative setup action; mcp serves JSON-RPC over stdio.")
		return cli.ExitCodeOK
	}
	if len(args) == 1 && (args[0] == "-v" || args[0] == "--version") {
		fmt.Fprintf(stdout, "kisage version %s\n", version)
		return cli.ExitCodeOK
	}
	if len(args) == 1 && args[0] == "init-store" {
		if err := host.InitializeStore(configPath); err != nil {
			fmt.Fprintln(stderr, err)
			return cli.ExitCodeError
		}
		return cli.ExitCodeOK
	}
	isMCP := len(args) == 1 && args[0] == "mcp"
	if !isMCP {
		if _, err := cli.ParseArgs(args); err != nil {
			return cli.ReportError(args, stdout, stderr, err)
		}
	}
	runtime, err := host.Open(configPath)
	if err != nil {
		if isMCP {
			fmt.Fprintln(stderr, err)
			return cli.ExitCodeError
		}
		return cli.ReportError(args, stdout, stderr, err)
	}
	defer runtime.Close()
	if isMCP {
		server, err := mcp.NewServer(runtime.Host)
		if err == nil {
			err = server.ServeStream(stdin, stdout)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return cli.ExitCodeError
		}
		return cli.ExitCodeOK
	}
	return cli.NewRunner(runtime.Host).Run(args, stdout, stderr)
}
