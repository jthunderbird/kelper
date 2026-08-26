package kubeconfig

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jthunderbird/kelper/internal/output"
	"github.com/spf13/cobra"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// AccountType represents the kubeconfig account type.
type AccountType string

const (
	AccountTypeReadonly AccountType = "readonly"
	AccountTypeAdmin    AccountType = "admin"
	AccountTypeScoped   AccountType = "scoped"
)

// Options holds the parameters for kubeconfig generation.
type Options struct {
	AccountType AccountType
	Username    string
	// Namespace scopes the generated RBAC to a single namespace. When empty
	// the account is cluster-wide (ClusterRole + ClusterRoleBinding).
	Namespace      string
	Resources      []string
	APIGroups      []string
	Verbs          []string
	OutputFile     string
	SkipConfirm    bool
	KubeconfigPath string
}

// ClusterWide reports whether the account applies to every namespace.
func (o Options) ClusterWide() bool {
	return o.Namespace == ""
}

// usernamePattern matches usernames that are safe to embed in RBAC object
// names, which must be valid RFC 1123 subdomains.
var usernamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidateUsername returns an error if name cannot be used to build valid
// Kubernetes object names for the generated Role and RoleBinding.
func ValidateUsername(name string) error {
	// Object names get a "-kelper-binding" suffix (15 chars) and must stay
	// within the 253-character RFC 1123 subdomain limit.
	const maxUsernameLen = 253 - len("-kelper-binding")
	if len(name) > maxUsernameLen {
		return fmt.Errorf("username %q is too long (max %d characters)", name, maxUsernameLen)
	}
	if !usernamePattern.MatchString(name) {
		return fmt.Errorf("invalid username %q: must be lowercase alphanumeric characters or '-', and must start and end with an alphanumeric character", name)
	}
	return nil
}

// GenerateUsername returns a unique username for the given account type, in
// the form "<account-type>-<8 hex characters>".
func GenerateUsername(accountType AccountType) (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate username: %w", err)
	}
	return fmt.Sprintf("%s-%s", accountType, hex.EncodeToString(buf)), nil
}

// ValidateOptions returns an error if required options are missing or invalid.
// Username and Namespace are both optional: an absent username is generated,
// and an absent namespace means the account is cluster-wide.
func ValidateOptions(opts Options) error {
	if opts.Username != "" {
		if err := ValidateUsername(opts.Username); err != nil {
			return err
		}
	}
	if opts.AccountType == AccountTypeScoped {
		if len(opts.Resources) == 0 {
			return fmt.Errorf("--resources is required for scoped account type")
		}
		if len(opts.Verbs) == 0 {
			return fmt.Errorf("--verbs cannot be empty for scoped account type")
		}
		if len(opts.APIGroups) == 0 {
			return fmt.Errorf("--apigroups cannot be empty for scoped account type")
		}
	}
	return nil
}

// coreAPIGroupAlias is how the core (legacy, unnamed) API group is spelled on
// the command line, since an empty string is indistinguishable from an unset
// flag once the comma-separated list is split.
const coreAPIGroupAlias = "core"

// ParseAPIGroups splits a comma-separated API group list, translating the
// "core" alias to the empty-string core group. An empty list becomes "*".
func ParseAPIGroups(s string) []string {
	groups := splitCSV(s)
	if len(groups) == 0 {
		return []string{"*"}
	}
	for i, g := range groups {
		if g == coreAPIGroupAlias {
			groups[i] = ""
		}
	}
	return groups
}

// GenerateRSAKey generates a 4096-bit RSA private key and returns PEM bytes.
// The key never touches disk.
func GenerateRSAKey() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("generate RSA key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), nil
}

// Command returns the cobra command tree for kubeconfig. kubeconfigPath points
// at the value of the root --kubeconfig flag, which is resolved after flag
// parsing, so it is dereferenced only once a subcommand runs.
func Command(kubeconfigPath *string) *cobra.Command {
	root := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate kubeconfig files for cluster users",
		Long: "Generate kubeconfig files for cluster users.\n\n" +
			"Accounts are cluster-wide by default. Pass --namespace to scope the\n" +
			"generated Role and RoleBinding to a single namespace instead.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(*kubeconfigPath)
		},
	}
	root.AddCommand(
		readonlyCmd(kubeconfigPath),
		adminCmd(kubeconfigPath),
		scopedCmd(kubeconfigPath),
	)
	return root
}

func readonlyCmd(kubeconfigPath *string) *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeReadonly
	cmd := &cobra.Command{
		Use:   "readonly",
		Short: "Create a readonly kubeconfig (cluster-wide, or -n for one namespace)",
		RunE:  runNonInteractive(&opts, kubeconfigPath),
	}
	addCommonFlags(cmd, &opts)
	return cmd
}

func adminCmd(kubeconfigPath *string) *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeAdmin
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Create an admin kubeconfig (cluster-wide, or -n for one namespace)",
		RunE:  runNonInteractive(&opts, kubeconfigPath),
	}
	addCommonFlags(cmd, &opts)
	return cmd
}

func scopedCmd(kubeconfigPath *string) *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeScoped
	var resourcesStr, apiGroupsStr, verbsStr string
	cmd := &cobra.Command{
		Use:   "scoped",
		Short: "Create a resource-scoped kubeconfig (cluster-wide, or -n for one namespace)",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Resources = splitCSV(resourcesStr)
			opts.APIGroups = ParseAPIGroups(apiGroupsStr)
			opts.Verbs = splitCSV(verbsStr)
			return runNonInteractive(&opts, kubeconfigPath)(cmd, args)
		},
	}
	addCommonFlags(cmd, &opts)
	cmd.Flags().StringVar(&resourcesStr, "resources", "", "comma-separated resources (required)")
	cmd.Flags().StringVar(&apiGroupsStr, "apigroups", "*", `comma-separated API groups ("core" for the core group, "*" for all)`)
	cmd.Flags().StringVar(&verbsStr, "verbs", "get,list,watch", "comma-separated verbs")
	return cmd
}

func addCommonFlags(cmd *cobra.Command, opts *Options) {
	cmd.Flags().StringVar(&opts.Username, "user", "", "username (default: generated, e.g. "+string(opts.AccountType)+"-1a2b3c4d)")
	cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "", "restrict the account to a single namespace (default: cluster-wide)")
	cmd.Flags().StringVar(&opts.OutputFile, "output", "", "output file (default: stdout)")
	cmd.Flags().BoolVarP(&opts.SkipConfirm, "yes", "y", false, "skip confirmation prompt")
}

func runNonInteractive(opts *Options, kubeconfigPath *string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if kubeconfigPath != nil {
			opts.KubeconfigPath = *kubeconfigPath
		}
		if err := ValidateOptions(*opts); err != nil {
			output.Errorf(os.Stderr, "%s", err)
			os.Exit(1)
		}
		if opts.Username == "" {
			generated, err := GenerateUsername(opts.AccountType)
			if err != nil {
				output.Errorf(os.Stderr, "%s", err)
				os.Exit(1)
			}
			opts.Username = generated
		}
		printSummary(*opts)
		if !opts.SkipConfirm {
			fmt.Print("Proceed? [y/N]: ")
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Scan()
			answer := strings.TrimSpace(scanner.Text())
			if !strings.EqualFold(answer, "y") {
				fmt.Println("Cancelled.")
				return nil
			}
		}
		return Create(*opts)
	}
}

func printSummary(opts Options) {
	fmt.Println("\nSummary:")
	fmt.Printf("  Type:      %s\n", opts.AccountType)
	fmt.Printf("  Username:  %s\n", opts.Username)
	if opts.ClusterWide() {
		fmt.Printf("  Scope:     cluster-wide (all namespaces)\n")
	} else {
		fmt.Printf("  Scope:     namespace %s\n", opts.Namespace)
	}
	if len(opts.Verbs) > 0 {
		fmt.Printf("  Verbs:     %s\n", strings.Join(opts.Verbs, ", "))
	}
	if len(opts.Resources) > 0 {
		fmt.Printf("  Resources: %s\n", strings.Join(opts.Resources, ", "))
	}
	if len(opts.APIGroups) > 0 {
		fmt.Printf("  APIGroups: %s\n", strings.Join(displayAPIGroups(opts.APIGroups), ", "))
	}
	if opts.OutputFile != "" {
		fmt.Printf("  Output:    %s\n", opts.OutputFile)
	} else {
		fmt.Printf("  Output:    stdout\n")
	}
	fmt.Println()
}

// Create executes the full kubeconfig generation flow.
func Create(opts Options) error {
	cs, err := buildClientset(opts.KubeconfigPath)
	if err != nil {
		return fmt.Errorf("connect to cluster: %w", err)
	}
	ctx := context.Background()

	fmt.Print("Generating RSA key... ")
	keyPEM, err := GenerateRSAKey()
	if err != nil {
		return err
	}
	fmt.Println("done")

	fmt.Print("Submitting and approving CSR... ")
	certPEM, err := submitAndApproveCSR(ctx, cs, opts.Username, keyPEM)
	if err != nil {
		return err
	}
	fmt.Println("done")

	fmt.Print("Creating RBAC... ")
	if err := createRBAC(ctx, cs, opts); err != nil {
		return err
	}
	fmt.Println("done")

	fmt.Print("Building kubeconfig... ")
	kubeconfigYAML, err := buildKubeconfig(opts, certPEM, keyPEM)
	if err != nil {
		return err
	}
	fmt.Println("done")

	if opts.OutputFile != "" {
		if err := os.WriteFile(opts.OutputFile, kubeconfigYAML, 0600); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		fmt.Printf("Kubeconfig written to %s\n", opts.OutputFile)
	} else {
		fmt.Print(string(kubeconfigYAML))
	}
	return nil
}

func submitAndApproveCSR(ctx context.Context, cs *kubernetes.Clientset, username string, keyPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(keyPEM)
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   username,
			Organization: []string{"kelper"},
		},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, key)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	csrName := username + "-kelper-csr"
	// Delete any pre-existing CSR.
	_ = cs.CertificatesV1().CertificateSigningRequests().Delete(ctx, csrName, metav1.DeleteOptions{})

	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: csrName},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kube-apiserver-client",
			Usages:     []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
	created, err := cs.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create CSR resource: %w", err)
	}

	// Approve.
	created.Status.Conditions = append(created.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateApproved,
		Status:         corev1.ConditionTrue,
		Reason:         "KelperApprove",
		Message:        "Approved by kelper",
		LastUpdateTime: metav1.Now(),
	})
	_, err = cs.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csrName, created, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("approve CSR: %w", err)
	}

	// Poll for signed cert (up to 10 seconds).
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		approved, err := cs.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get CSR status: %w", err)
		}
		if len(approved.Status.Certificate) > 0 {
			return approved.Status.Certificate, nil
		}
	}
	return nil, fmt.Errorf("timed out waiting for signed certificate")
}

// policyRule returns the single RBAC rule implied by the account type. For
// scoped accounts the caller-supplied resources, API groups and verbs are used
// as-is.
func policyRule(opts Options) rbacv1.PolicyRule {
	switch opts.AccountType {
	case AccountTypeReadonly:
		return rbacv1.PolicyRule{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch"}}
	case AccountTypeAdmin:
		return rbacv1.PolicyRule{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}
	default:
		return rbacv1.PolicyRule{APIGroups: opts.APIGroups, Resources: opts.Resources, Verbs: opts.Verbs}
	}
}

// createRBAC creates or updates the Role/ClusterRole and matching binding for
// the account. Re-running for an existing username refreshes its permissions
// in place, mirroring how the CSR is recreated on every run.
func createRBAC(ctx context.Context, cs *kubernetes.Clientset, opts Options) error {
	rule := policyRule(opts)
	roleName := opts.Username + "-kelper-role"
	bindingName := opts.Username + "-kelper-binding"
	subjects := []rbacv1.Subject{{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: opts.Username}}

	if opts.ClusterWide() {
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: roleName},
			Rules:      []rbacv1.PolicyRule{rule},
		}
		if err := upsert(
			func() error {
				_, err := cs.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
				return err
			},
			func() error {
				_, err := cs.RbacV1().ClusterRoles().Update(ctx, cr, metav1.UpdateOptions{})
				return err
			},
		); err != nil {
			return fmt.Errorf("create ClusterRole: %w", err)
		}

		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: bindingName},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
			Subjects:   subjects,
		}
		// RoleRef is immutable, so an existing binding is replaced rather than
		// updated in place.
		if err := upsert(
			func() error {
				_, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
				return err
			},
			func() error {
				if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, bindingName, metav1.DeleteOptions{}); err != nil {
					return err
				}
				_, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
				return err
			},
		); err != nil {
			return fmt.Errorf("create ClusterRoleBinding: %w", err)
		}
		return nil
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: opts.Namespace},
		Rules:      []rbacv1.PolicyRule{rule},
	}
	if err := upsert(
		func() error {
			_, err := cs.RbacV1().Roles(opts.Namespace).Create(ctx, role, metav1.CreateOptions{})
			return err
		},
		func() error {
			_, err := cs.RbacV1().Roles(opts.Namespace).Update(ctx, role, metav1.UpdateOptions{})
			return err
		},
	); err != nil {
		return fmt.Errorf("create Role: %w", err)
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: opts.Namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: roleName},
		Subjects:   subjects,
	}
	if err := upsert(
		func() error {
			_, err := cs.RbacV1().RoleBindings(opts.Namespace).Create(ctx, rb, metav1.CreateOptions{})
			return err
		},
		func() error {
			if err := cs.RbacV1().RoleBindings(opts.Namespace).Delete(ctx, bindingName, metav1.DeleteOptions{}); err != nil {
				return err
			}
			_, err := cs.RbacV1().RoleBindings(opts.Namespace).Create(ctx, rb, metav1.CreateOptions{})
			return err
		},
	); err != nil {
		return fmt.Errorf("create RoleBinding: %w", err)
	}
	return nil
}

// upsert runs create, falling back to replace when the object already exists.
func upsert(create, replace func() error) error {
	err := create()
	if apierrors.IsAlreadyExists(err) {
		return replace()
	}
	return err
}

// buildKubeconfig renders a kubeconfig for the new user, reusing the server
// and CA of the cluster referenced by the current context.
func buildKubeconfig(opts Options, certPEM, keyPEM []byte) ([]byte, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.KubeconfigPath != "" {
		rules.ExplicitPath = opts.KubeconfigPath
	}
	rawConfig, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	clusterName, cluster, err := currentCluster(rawConfig)
	if err != nil {
		return nil, err
	}

	caData := cluster.CertificateAuthorityData
	if len(caData) == 0 && cluster.CertificateAuthority != "" {
		// The source kubeconfig references the CA by path; inline it so the
		// generated file stands alone.
		caData, err = os.ReadFile(cluster.CertificateAuthority)
		if err != nil {
			return nil, fmt.Errorf("read certificate authority %s: %w", cluster.CertificateAuthority, err)
		}
	}

	contextName := opts.Username + "@" + clusterName
	kubeContext := &clientcmdapi.Context{
		Cluster:  clusterName,
		AuthInfo: opts.Username,
	}
	// A namespace-scoped account can only read its own namespace, so make that
	// the context default instead of "default".
	if !opts.ClusterWide() {
		kubeContext.Namespace = opts.Namespace
	}

	cfg := clientcmdapi.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterName: {
				Server:                   cluster.Server,
				CertificateAuthorityData: caData,
				InsecureSkipTLSVerify:    cluster.InsecureSkipTLSVerify,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			opts.Username: {
				ClientCertificateData: certPEM,
				ClientKeyData:         keyPEM,
			},
		},
		Contexts:       map[string]*clientcmdapi.Context{contextName: kubeContext},
		CurrentContext: contextName,
	}

	// clientcmd.Write converts the in-memory (map-keyed) config to the v1
	// on-disk schema, whose clusters/contexts/users are lists. Marshalling the
	// api.Config directly emits maps that kubectl cannot load.
	out, err := clientcmd.Write(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal kubeconfig: %w", err)
	}
	return out, nil
}

// currentCluster returns the cluster referenced by the config's current
// context. Iterating the Clusters map directly is non-deterministic when more
// than one cluster is defined.
func currentCluster(cfg *clientcmdapi.Config) (string, *clientcmdapi.Cluster, error) {
	if cfg.CurrentContext == "" {
		return "", nil, fmt.Errorf("kubeconfig has no current context")
	}
	kubeContext, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		return "", nil, fmt.Errorf("current context %q not found in kubeconfig", cfg.CurrentContext)
	}
	cluster, ok := cfg.Clusters[kubeContext.Cluster]
	if !ok {
		return "", nil, fmt.Errorf("cluster %q referenced by context %q not found in kubeconfig", kubeContext.Cluster, cfg.CurrentContext)
	}
	return kubeContext.Cluster, cluster, nil
}

func buildClientset(kubeconfigPath string) (*kubernetes.Clientset, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

// displayAPIGroups renders the core group as its "core" alias so summaries do
// not show a blank entry.
func displayAPIGroups(groups []string) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		if g == "" {
			g = coreAPIGroupAlias
		}
		out[i] = g
	}
	return out
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
