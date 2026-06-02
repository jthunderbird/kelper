package kubeconfig

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jthunderbird/kelper/internal/output"
	"github.com/spf13/cobra"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

// AccountType represents the kubeconfig account type.
type AccountType string

const (
	AccountTypeReadonly AccountType = "readonly"
	AccountTypeAdmin    AccountType = "admin"
	AccountTypeCluster  AccountType = "cluster"
	AccountTypeScoped   AccountType = "scoped"
)

// Options holds the parameters for kubeconfig generation.
type Options struct {
	AccountType AccountType
	Username    string
	Namespace   string
	Resources   []string
	APIGroups   []string
	Verbs       []string
	OutputFile  string
	SkipConfirm bool
}

// ValidateOptions returns an error if required options are missing.
func ValidateOptions(opts Options) error {
	if opts.Username == "" {
		return fmt.Errorf("--user is required")
	}
	switch opts.AccountType {
	case AccountTypeReadonly, AccountTypeAdmin:
		if opts.Namespace == "" {
			return fmt.Errorf("--namespace is required for %s account type", opts.AccountType)
		}
	case AccountTypeScoped:
		if opts.Namespace == "" {
			return fmt.Errorf("--namespace is required for scoped account type")
		}
		if len(opts.Resources) == 0 {
			return fmt.Errorf("--resources is required for scoped account type")
		}
	case AccountTypeCluster:
		// No namespace required.
	}
	return nil
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

// Command returns the cobra command tree for kubeconfig.
func Command() *cobra.Command {
	root := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate kubeconfig files for cluster users",
		RunE: func(cmd *cobra.Command, args []string) error {
			// No subcommand → TUI wizard (implemented in Task 11).
			return runTUI()
		},
	}
	root.AddCommand(readonlyCmd(), adminCmd(), clusterCmd(), scopedCmd())
	return root
}

func readonlyCmd() *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeReadonly
	cmd := &cobra.Command{
		Use:   "readonly",
		Short: "Create a namespace-scoped readonly kubeconfig",
		RunE:  runNonInteractive(&opts),
	}
	addCommonFlags(cmd, &opts)
	return cmd
}

func adminCmd() *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeAdmin
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Create a namespace-scoped admin kubeconfig",
		RunE:  runNonInteractive(&opts),
	}
	addCommonFlags(cmd, &opts)
	return cmd
}

func clusterCmd() *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeCluster
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Create a cluster-wide readonly kubeconfig",
		RunE:  runNonInteractive(&opts),
	}
	cmd.Flags().StringVar(&opts.Username, "user", "", "username (required)")
	cmd.Flags().StringVar(&opts.OutputFile, "output", "", "output file (default: stdout)")
	cmd.Flags().BoolVarP(&opts.SkipConfirm, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func scopedCmd() *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeScoped
	var resourcesStr, apiGroupsStr, verbsStr string
	cmd := &cobra.Command{
		Use:   "scoped",
		Short: "Create a resource-scoped kubeconfig",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Resources = splitCSV(resourcesStr)
			opts.APIGroups = splitCSV(apiGroupsStr)
			opts.Verbs = splitCSV(verbsStr)
			return runNonInteractive(&opts)(cmd, args)
		},
	}
	addCommonFlags(cmd, &opts)
	cmd.Flags().StringVar(&resourcesStr, "resources", "", "comma-separated resources (required)")
	cmd.Flags().StringVar(&apiGroupsStr, "apigroups", "*", "comma-separated API groups")
	cmd.Flags().StringVar(&verbsStr, "verbs", "get,list,watch", "comma-separated verbs")
	return cmd
}

func addCommonFlags(cmd *cobra.Command, opts *Options) {
	cmd.Flags().StringVar(&opts.Username, "user", "", "username (required)")
	cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "", "namespace (required)")
	cmd.Flags().StringVar(&opts.OutputFile, "output", "", "output file (default: stdout)")
	cmd.Flags().BoolVarP(&opts.SkipConfirm, "yes", "y", false, "skip confirmation prompt")
}

func runNonInteractive(opts *Options) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := ValidateOptions(*opts); err != nil {
			output.Errorf(os.Stderr, "%s", err)
			os.Exit(1)
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
	if opts.Namespace != "" {
		fmt.Printf("  Namespace: %s\n", opts.Namespace)
	}
	if len(opts.Verbs) > 0 {
		fmt.Printf("  Verbs:     %s\n", strings.Join(opts.Verbs, ", "))
	}
	if len(opts.Resources) > 0 {
		fmt.Printf("  Resources: %s\n", strings.Join(opts.Resources, ", "))
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
	cs, err := buildClientset()
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
	kubeconfigYAML, err := buildKubeconfig(opts.Username, certPEM, keyPEM)
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

func createRBAC(ctx context.Context, cs *kubernetes.Clientset, opts Options) error {
	verbs := opts.Verbs
	resources := opts.Resources
	apiGroups := opts.APIGroups

	switch opts.AccountType {
	case AccountTypeReadonly:
		verbs = []string{"get", "list", "watch"}
		resources = []string{"*"}
		apiGroups = []string{"*"}
	case AccountTypeAdmin:
		verbs = []string{"*"}
		resources = []string{"*"}
		apiGroups = []string{"*"}
	case AccountTypeCluster:
		verbs = []string{"get", "list", "watch"}
		resources = []string{"*"}
		apiGroups = []string{"*"}
	}

	roleName := opts.Username + "-kelper-role"
	bindingName := opts.Username + "-kelper-binding"

	if opts.AccountType == AccountTypeCluster {
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: roleName},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: apiGroups, Resources: resources, Verbs: verbs},
			},
		}
		if _, err := cs.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create ClusterRole: %w", err)
		}
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: bindingName},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
			Subjects:   []rbacv1.Subject{{Kind: "User", Name: opts.Username}},
		}
		if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create ClusterRoleBinding: %w", err)
		}
		return nil
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: opts.Namespace},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: apiGroups, Resources: resources, Verbs: verbs},
		},
	}
	if _, err := cs.RbacV1().Roles(opts.Namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create Role: %w", err)
	}
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: opts.Namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: roleName},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: opts.Username}},
	}
	if _, err := cs.RbacV1().RoleBindings(opts.Namespace).Create(ctx, rb, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create RoleBinding: %w", err)
	}
	return nil
}

func buildKubeconfig(username string, certPEM, keyPEM []byte) ([]byte, error) {
	rawConfig, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	var server string
	var caData []byte
	for _, cluster := range rawConfig.Clusters {
		server = cluster.Server
		caData = cluster.CertificateAuthorityData
		break
	}

	cfg := &clientcmdapi.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*clientcmdapi.Cluster{
			"kelper-cluster": {
				Server:                   server,
				CertificateAuthorityData: caData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			username: {
				ClientCertificateData: certPEM,
				ClientKeyData:         keyPEM,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			username + "@kelper-cluster": {
				Cluster:  "kelper-cluster",
				AuthInfo: username,
			},
		},
		CurrentContext: username + "@kelper-cluster",
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal kubeconfig: %w", err)
	}
	return out, nil
}

func buildClientset() (*kubernetes.Clientset, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
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

// runTUI is the placeholder for the interactive wizard.
// Implemented in tui.go (Task 11).
func runTUI() error {
	fmt.Println("kubeconfig TUI not yet implemented — use a subcommand:")
	fmt.Println("  kelper kubeconfig readonly --user <name> --namespace <ns>")
	fmt.Println("  kelper kubeconfig admin    --user <name> --namespace <ns>")
	fmt.Println("  kelper kubeconfig cluster  --user <name>")
	fmt.Println("  kelper kubeconfig scoped   --user <name> --namespace <ns> --resources <r>")
	return nil
}
