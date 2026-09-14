// Command seaglass is a Kubernetes TUI.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/go-logr/logr"
	"k8s.io/klog/v2"

	"github.com/ctrl-research/seaglass/internal/app"
	"github.com/ctrl-research/seaglass/internal/config"
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

	// client-go logs via klog to stderr, which would corrupt the TUI. Route
	// every klog path (including contextual/structured) to a discard logger.
	klog.SetOutput(io.Discard)
	klog.LogToStderr(false)
	klog.SetLogger(logr.Discard())

	closeLog, err := setupLogging(debug)
	if err != nil {
		return err
	}
	defer closeLog()

	// Flags win over remembered state, which wins over kubeconfig defaults.
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	saved, err := store.Load()
	if err != nil {
		slog.Warn("ignoring unreadable state", "err", err)
	}
	if kubeContext == "" && saved.LastContext != "" {
		if names, _, err := k8s.ListContexts(); err == nil && slices.Contains(names, saved.LastContext) {
			kubeContext = saved.LastContext
		}
	}

	client, err := k8s.New(kubeContext, namespace)
	if err != nil {
		return err
	}
	ns := client.Namespace
	res := k8s.Pods
	if cs, ok := saved.For(client.Context); ok {
		if namespace == "" && !allNS {
			if cs.AllNamespaces {
				ns = ""
			} else if cs.Namespace != "" {
				ns = cs.Namespace
			}
		}
		if cs.Resource != nil {
			res = *cs.Resource
		}
	}
	if allNS {
		ns = ""
	}
	slog.Info("starting", "version", version, "context", client.Context, "namespace", ns, "resource", res.Name())

	m := app.New(app.Options{
		Client:    client,
		Namespace: ns,
		Resource:  res,
		Version:   version,
		State:     store,
		SaveDir:   ".",
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
	dir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "seaglass.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { _ = f.Close() }, nil
}
