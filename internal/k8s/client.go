// Package k8s wraps client-go for seaglass. It exposes a REST client that
// speaks the server-side Table protocol so every resource type renders
// through one code path.
package k8s

import (
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	// Register auth plugins (oidc, azure, gcp). Exec auth is built in.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

// Client is a connection to one cluster under one kube context.
type Client struct {
	// Context is the resolved kube context name.
	Context string
	// Namespace is the default namespace for the context, or the override.
	Namespace string
	// Host is the API server URL.
	Host string
	// User is the kubeconfig user (AuthInfo) name for the context.
	User string

	cfg  *rest.Config
	rest *rest.RESTClient
}

// ListContexts returns the context names in kubeconfig and the current one.
func ListContexts() (names []string, current string, err error) {
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	for name := range raw.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, raw.CurrentContext, nil
}

// New loads kubeconfig using the standard loading rules ($KUBECONFIG, then
// ~/.kube/config) and returns a client for the given context. An empty
// kubeContext selects the current context. An empty namespace selects the
// context's default namespace.
func New(kubeContext, namespace string) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	if namespace != "" {
		overrides.Context.Namespace = namespace
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	raw, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	ctxName := kubeContext
	if ctxName == "" {
		ctxName = raw.CurrentContext
	}
	if ctxName == "" {
		return nil, fmt.Errorf("no current context in kubeconfig; pass --context")
	}
	if _, ok := raw.Contexts[ctxName]; !ok {
		return nil, fmt.Errorf("context %q not found in kubeconfig", ctxName)
	}

	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("build client config: %w", err)
	}
	ns, _, err := cc.Namespace()
	if err != nil {
		return nil, fmt.Errorf("resolve namespace: %w", err)
	}

	rc := dynamic.ConfigFor(cfg)
	rc.GroupVersion = &schema.GroupVersion{}
	rc.APIPath = "/if-you-see-this-search-for-the-break"
	rc.UserAgent = "seaglass"
	rc.Timeout = 0 // watches are long-lived; per-request timeouts are set explicitly
	rest, err := rest.RESTClientFor(rc)
	if err != nil {
		return nil, fmt.Errorf("build rest client: %w", err)
	}

	return &Client{
		Context:   ctxName,
		Namespace: ns,
		Host:      cfg.Host,
		User:      raw.Contexts[ctxName].AuthInfo,
		cfg:       cfg,
		rest:      rest,
	}, nil
}

// resourcePath returns the URL path segments for a resource collection.
// An empty namespace means cluster scope or all namespaces.
func resourcePath(gvr schema.GroupVersionResource, namespace string) []string {
	var segs []string
	if gvr.Group == "" {
		segs = append(segs, "api", gvr.Version)
	} else {
		segs = append(segs, "apis", gvr.Group, gvr.Version)
	}
	if namespace != "" {
		segs = append(segs, "namespaces", namespace)
	}
	return append(segs, gvr.Resource)
}
