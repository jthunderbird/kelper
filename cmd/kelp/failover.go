package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

// defaultProbeTimeout is how long to wait for a TCP connection to an
// api-server endpoint before considering it down.
const defaultProbeTimeout = 2 * time.Second

// resolveKubeconfig loads the kubeconfig (from the given path, or the standard
// discovery rules when empty) and resolves the current context's cluster
// server. When the server field holds a comma-delimited list of endpoints it
// probes each in order, logging fallbacks to stdout, and writes a temporary
// kubeconfig pointing at the first reachable endpoint. This effectively gives
// the binary client-side api-server load balancing, which plain kubeconfig
// (one and only one server) cannot express.
//
// It returns a kubeconfig path that downstream tooling (kubectl or client-go)
// can consume, a cleanup func that removes any temp file, and an error only
// when every configured endpoint is unreachable.
func resolveKubeconfig(kubeconfigPath string) (string, func(), error) {
	noop := func() {}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{})

	rawConfig, err := clientConfig.RawConfig()
	if err != nil {
		// No usable kubeconfig - leave existing behavior unchanged.
		return kubeconfigPath, noop, nil
	}

	kubeContext, ok := rawConfig.Contexts[rawConfig.CurrentContext]
	if !ok {
		return kubeconfigPath, noop, nil
	}

	cluster, ok := rawConfig.Clusters[kubeContext.Cluster]
	if !ok {
		return kubeconfigPath, noop, nil
	}

	endpoints := splitServers(cluster.Server)
	if len(endpoints) <= 1 {
		// Single (or no) server - nothing to load balance.
		return kubeconfigPath, noop, nil
	}

	chosen, err := firstReachable(endpoints, defaultProbeTimeout)
	if err != nil {
		return "", noop, err
	}

	// Point the active cluster at the chosen endpoint and persist a temporary
	// kubeconfig for downstream consumers, which only accept a single server.
	cluster.Server = chosen
	rawConfig.Clusters[kubeContext.Cluster] = cluster

	tmp, err := os.CreateTemp("", "kelp-kubeconfig-*.yaml")
	if err != nil {
		return "", noop, fmt.Errorf("creating temp kubeconfig: %w", err)
	}
	tmp.Close()

	if err := clientcmd.WriteToFile(rawConfig, tmp.Name()); err != nil {
		os.Remove(tmp.Name())
		return "", noop, fmt.Errorf("writing temp kubeconfig: %w", err)
	}

	cleanup := func() { os.Remove(tmp.Name()) }
	return tmp.Name(), cleanup, nil
}

// splitServers parses a (possibly comma-delimited) server field into a list of
// trimmed, non-empty endpoint URLs.
func splitServers(server string) []string {
	var out []string
	for _, s := range strings.Split(server, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// firstReachable probes endpoints in order and returns the first that accepts a
// TCP connection. Each unreachable endpoint is logged to stdout before the next
// is tried. It errors only when the list is exhausted.
func firstReachable(endpoints []string, timeout time.Duration) (string, error) {
	for i, endpoint := range endpoints {
		if err := probe(endpoint, timeout); err != nil {
			fmt.Printf("api-server %s unreachable (%v); trying next endpoint...\n", endpoint, err)
			continue
		}
		if i > 0 {
			fmt.Printf("api-server %s reachable; using it\n", endpoint)
		}
		return endpoint, nil
	}
	return "", fmt.Errorf("not connected: all %d api-server endpoints unreachable", len(endpoints))
}

// probe opens a short-lived TCP connection to the endpoint's host:port to
// determine whether the api-server is up. A missing port defaults to 443.
func probe(endpoint string, timeout time.Duration) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid server url: %w", err)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
