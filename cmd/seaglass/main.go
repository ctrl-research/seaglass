// Command seaglass is a Kubernetes TUI.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/ctrl-research/seaglass/internal/app"
	"github.com/ctrl-research/seaglass/internal/k8s"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seaglass:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		kubeContext string
		namespace   string
		allNS       bool
		debug       bool
		showVersion bool
	)
	flag.StringVar(&kubeContext, "context", "", "kube context (default: current)")
	flag.StringVar(&namespace, "namespace", "", "namespace (default: context default)")
	flag.StringVar(&namespace, "n", "", "shorthand for --namespace")
	flag.BoolVar(&allNS, "all-namespaces", false, "list across all namespaces")
	flag.BoolVar(&allNS, "A", false, "shorthand for --all-namespaces")
	flag.BoolVar(&debug, "debug", false, "write a debug log to the state dir")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		return nil
	}

	// client-go logs via klog to stderr, which would corrupt the TUI.
	klog.SetOutput(io.Discard)
	klog.LogToStderr(false)

	closeLog, err := setupLogging(debug)
	if err != nil {
		return err
	}
	defer closeLog()

	client, err := k8s.New(kubeContext, namespace)
	if err != nil {
		return err
	}
	ns := client.Namespace
	if allNS {
		ns = ""
	}
	slog.Info("starting", "version", version, "context", client.Context, "namespace", ns)

	m := app.New(app.Options{
		Client:    client,
		Namespace: ns,
		Resource:  schema.GroupVersionResource{Version: "v1", Resource: "pods"},
	})
	_, err = tea.NewProgram(m).Run()
	return err
}

// setupLogging routes slog to a file when debug is on, and discards it
// otherwise. Nothing may ever be written to the terminal while the TUI runs.
func setupLogging(debug bool) (func(), error) {
	if !debug {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return func() {}, nil
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	dir = filepath.Join(dir, "seaglass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "seaglass.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { _ = f.Close() }, nil
}
