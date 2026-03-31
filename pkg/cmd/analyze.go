package cmd

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type AnalyzeOptions struct {
	BundlePath   string
	OutputDir    string
	IncludeGraph bool
	UseAI        bool
	ServiceType  string
}

type AnalyzeBundle struct {
	BundlePath string            `json:"bundle_path"`
	RootDir    string            `json:"-"`
	PodName    string            `json:"pod_name,omitempty"`
	Files      map[string]string `json:"-"`
	Logs       map[string]string `json:"-"`
	JSONDocs   map[string]any    `json:"-"`
}

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarn     Severity = "warn"
	SeverityCritical Severity = "critical"
)

type Evidence struct {
	File    string `json:"file"`
	Pointer string `json:"pointer,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

type Finding struct {
	ID                 string     `json:"id"`
	Title              string     `json:"title"`
	Severity           Severity   `json:"severity"`
	Confidence         float64    `json:"confidence"`
	Summary            string     `json:"summary"`
	Hypothesis         string     `json:"hypothesis,omitempty"`
	Evidence           []Evidence `json:"evidence,omitempty"`
	RecommendedActions []string   `json:"recommended_actions,omitempty"`
	Tags               []string   `json:"tags,omitempty"`
}

type ReportSummary struct {
	TotalFindings    int `json:"total_findings"`
	CriticalFindings int `json:"critical_findings"`
	WarningFindings  int `json:"warning_findings"`
	InfoFindings     int `json:"info_findings"`
}

type AnalyzeReport struct {
	Version       string        `json:"version"`
	GeneratedAt   time.Time     `json:"generated_at"`
	BundlePath    string        `json:"bundle_path"`
	PodName       string        `json:"pod_name,omitempty"`
	GraphIncluded bool          `json:"graph_included"`
	Summary       ReportSummary `json:"summary"`
	Findings      []Finding     `json:"findings"`
}

type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type Rule interface {
	ID() string
	Evaluate(*AnalyzeBundle) []Finding
}

type ParsedCluster struct {
	Name              string                  `json:"name"`
	ObservabilityName string                  `json:"observability_name,omitempty"`
	AddedViaAPI       *bool                   `json:"added_via_api,omitempty"`
	DefaultPriority   map[string]string       `json:"default_priority,omitempty"`
	HighPriority      map[string]string       `json:"high_priority,omitempty"`
	Endpoints         map[string]*ClusterHost `json:"endpoints,omitempty"`
}

type ClusterHost struct {
	Address                string            `json:"address"`
	Stats                  map[string]string `json:"stats,omitempty"`
	HealthFlags            string            `json:"health_flags,omitempty"`
	Hostname               string            `json:"hostname,omitempty"`
	Weight                 string            `json:"weight,omitempty"`
	Priority               string            `json:"priority,omitempty"`
	Region                 string            `json:"region,omitempty"`
	Zone                   string            `json:"zone,omitempty"`
	SubZone                string            `json:"sub_zone,omitempty"`
	Canary                 string            `json:"canary,omitempty"`
	SuccessRate            string            `json:"success_rate,omitempty"`
	LocalOriginSuccessRate string            `json:"local_origin_success_rate,omitempty"`
}

func NewAnalyzeCommand(_ genericclioptions.IOStreams) *cobra.Command {
	opts := &AnalyzeOptions{}

	cmd := &cobra.Command{
		Use:   "analyze [snapshot.tar.gz]",
		Short: "Analyze a captured xDSnap snapshot offline and emit findings/reports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BundlePath = args[0]

			if opts.UseAI {
				apiKey := os.Getenv("OPENAI_API_KEY")
				if apiKey == "" {
					return errors.New("AI analysis requested but OPENAI_API_KEY is not set")
				}
				// Reserved for future AI summarization over deterministic findings.
			}

			return runAnalyze(*opts)
		},
	}

	cmd.Flags().StringVarP(&opts.OutputDir, "output-dir", "o", "", "Directory to write report artifacts into")
	cmd.Flags().BoolVar(&opts.IncludeGraph, "graph", true, "Emit graph.json")
	cmd.Flags().BoolVar(&opts.UseAI, "ai", false, "Reserved for AI-assisted summarization after offline analysis")
	cmd.Flags().StringVar(&opts.ServiceType, "service-type", "", "Optional service type hint")

	return cmd
}

func runAnalyze(opts AnalyzeOptions) error {
	outputDir := opts.OutputDir
	if strings.TrimSpace(outputDir) == "" {
		base := filepath.Base(opts.BundlePath)
		base = strings.TrimSuffix(base, ".tar.gz")
		base = strings.TrimSuffix(base, ".tgz")
		outputDir = base + "_analysis"
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	extractedDir := filepath.Join(outputDir, "bundle")
	if err := extractTarGz(opts.BundlePath, extractedDir); err != nil {
		return fmt.Errorf("extract snapshot: %w", err)
	}

	bundle, err := loadAnalyzeBundle(opts.BundlePath, extractedDir)
	if err != nil {
		return fmt.Errorf("load bundle: %w", err)
	}

	findings := runRules(bundle)

	var graph *Graph
	if opts.IncludeGraph {
		graph = buildGraph(bundle)
	}

	report := buildReport(bundle, findings, graph)

	if err := writeFindingsJSONL(filepath.Join(outputDir, "findings.jsonl"), findings); err != nil {
		return fmt.Errorf("write findings.jsonl: %w", err)
	}
	if err := writeJSON(filepath.Join(outputDir, "report.json"), report); err != nil {
		return fmt.Errorf("write report.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "report.md"), []byte(renderMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write report.md: %w", err)
	}
	if graph != nil {
		if err := writeJSON(filepath.Join(outputDir, "graph.json"), graph); err != nil {
			return fmt.Errorf("write graph.json: %w", err)
		}
	}

	fmt.Printf("Analysis complete.\n")
	fmt.Printf("  report.md      -> %s\n", filepath.Join(outputDir, "report.md"))
	fmt.Printf("  report.json    -> %s\n", filepath.Join(outputDir, "report.json"))
	fmt.Printf("  findings.jsonl -> %s\n", filepath.Join(outputDir, "findings.jsonl"))
	if graph != nil {
		fmt.Printf("  graph.json     -> %s\n", filepath.Join(outputDir, "graph.json"))
	}

	return nil
}

func extractTarGz(bundlePath, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	file, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") {
			return fmt.Errorf("invalid tar entry: %s", header.Name)
		}

		targetPath := filepath.Join(destDir, cleanName)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			out, err := os.Create(targetPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

func loadAnalyzeBundle(bundlePath, root string) (*AnalyzeBundle, error) {
	b := &AnalyzeBundle{
		BundlePath: bundlePath,
		RootDir:    root,
		PodName:    inferPodNameFromBundle(bundlePath),
		Files:      map[string]string{},
		Logs:       map[string]string{},
		JSONDocs:   map[string]any{},
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)
		b.Files[rel] = content

		lower := strings.ToLower(rel)
		if strings.HasSuffix(lower, ".txt") || strings.Contains(lower, "logs") {
			b.Logs[rel] = content
		}
		if strings.HasSuffix(lower, ".json") {
			var doc any
			if json.Unmarshal(data, &doc) == nil {
				b.JSONDocs[rel] = doc
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return b, nil
}

func inferPodNameFromBundle(bundlePath string) string {
	base := filepath.Base(bundlePath)
	base = strings.TrimSuffix(base, ".tar.gz")
	base = strings.TrimSuffix(base, ".tgz")
	base = strings.TrimSuffix(base, "_snapshot")
	return base
}

func runRules(bundle *AnalyzeBundle) []Finding {
	rules := []Rule{
		NoHealthyUpstreamRule{},
		UnknownClusterRule{},
		PermissionDeniedRule{},
		XDSStreamClosedRule{},
		EnvoyInitialFetchTimeoutRule{},
		CertExpiryWarningRule{},
		MissingExpectedArtifactRule{},
		ClusterSemanticRule{},
	}

	var findings []Finding
	for _, rule := range rules {
		findings = append(findings, rule.Evaluate(bundle)...)
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return severityRank(findings[i].Severity) > severityRank(findings[j].Severity)
		}
		return findings[i].ID < findings[j].ID
	})

	return findings
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarn:
		return 2
	default:
		return 1
	}
}

type NoHealthyUpstreamRule struct{}

func (r NoHealthyUpstreamRule) ID() string { return "envoy.no_healthy_upstream" }
func (r NoHealthyUpstreamRule) Evaluate(b *AnalyzeBundle) []Finding {
	return grepLogsFinding(
		b,
		r.ID(),
		`(?i)no healthy upstream`,
		"Requests are failing with no healthy upstream",
		SeverityCritical,
		0.95,
		"Envoy could not route to any healthy upstream endpoints for at least one cluster.",
		[]string{
			"Inspect clusters.json and config_dump.json for the affected service cluster.",
			"Check whether the backing service has registered healthy endpoints.",
			"Verify Consul intentions, service-defaults, service-router, and gateway routing.",
		},
		[]string{"envoy", "routing", "upstream"},
	)
}

type UnknownClusterRule struct{}

func (r UnknownClusterRule) ID() string { return "envoy.unknown_cluster" }
func (r UnknownClusterRule) Evaluate(b *AnalyzeBundle) []Finding {
	return grepLogsFinding(
		b,
		r.ID(),
		`(?i)unknown cluster|cluster_not_found`,
		"Envoy referenced a cluster that does not exist",
		SeverityCritical,
		0.95,
		"The active route configuration appears to reference a cluster name that was not generated in the dataplane config.",
		[]string{
			"Compare route cluster names in config_dump.json against clusters.json.",
			"Check ServiceRouter, ServiceResolver, and HTTPRoute interactions.",
			"Verify whether subset-qualified clusters exist while the route references a base cluster name.",
		},
		[]string{"envoy", "clusters", "servicerouter", "httproute"},
	)
}

type PermissionDeniedRule struct{}

func (r PermissionDeniedRule) ID() string { return "consul.acl.permission_denied" }
func (r PermissionDeniedRule) Evaluate(b *AnalyzeBundle) []Finding {
	return grepLogsFinding(
		b,
		r.ID(),
		`(?i)permission denied|lacks permission|ACL not found|acl.*denied`,
		"ACL or token authorization issue detected",
		SeverityCritical,
		0.92,
		"A Consul ACL token, role binding, or identity mapping likely prevented the dataplane or control plane from reading or writing required resources.",
		[]string{
			"Inspect the referenced token, role, and policy attachments.",
			"Verify auth method and binding rule configuration if using OIDC/JWT/Kubernetes auth.",
			"Check namespace and partition scoping for the denied operation.",
		},
		[]string{"consul", "acl", "authz"},
	)
}

type XDSStreamClosedRule struct{}

func (r XDSStreamClosedRule) ID() string { return "xds.stream_closed" }
func (r XDSStreamClosedRule) Evaluate(b *AnalyzeBundle) []Finding {
	return grepLogsFinding(
		b,
		r.ID(),
		`(?i)DeltaAggregatedResources.*closed|AggregatedResources.*closed|xds.*stream.*closed|rpc error: code = Unavailable|error reading from server: EOF`,
		"xDS stream instability detected",
		SeverityWarn,
		0.85,
		"The xDS config stream closed, timed out, or repeatedly reconnected. This can cause stale proxy configuration or delayed warming.",
		[]string{
			"Check connectivity between dataplane and Consul servers or mesh gateways.",
			"Inspect TLS and certificate validity on both sides of the xDS connection.",
			"Review server logs for EOF, context canceled, or rate limiting around the same time.",
		},
		[]string{"xds", "envoy", "dataplane"},
	)
}

type EnvoyInitialFetchTimeoutRule struct{}

func (r EnvoyInitialFetchTimeoutRule) ID() string { return "envoy.initial_fetch_timeout" }
func (r EnvoyInitialFetchTimeoutRule) Evaluate(b *AnalyzeBundle) []Finding {
	return grepLogsFinding(
		b,
		r.ID(),
		`(?i)initial fetch timeout`,
		"Envoy initial fetch timeout observed",
		SeverityInfo,
		0.70,
		"Envoy reported an initial fetch timeout while warming config. This can be transient, but repeated occurrences may indicate xDS delivery lag or reachability issues.",
		[]string{
			"Correlate with xDS stream closures or control-plane connectivity errors.",
			"Check whether the timeout happened only during startup or throughout the incident.",
		},
		[]string{"envoy", "warming", "xds"},
	)
}

type CertExpiryWarningRule struct{}

func (r CertExpiryWarningRule) ID() string { return "tls.certificate_expiry" }
func (r CertExpiryWarningRule) Evaluate(b *AnalyzeBundle) []Finding {
	re := regexp.MustCompile(`(?i)cert(ificate)?[^\n]{0,100}(expired|not yet valid|expires?)`)
	var findings []Finding

	for file, content := range b.Files {
		loc := re.FindStringIndex(content)
		if loc == nil {
			continue
		}

		findings = append(findings, Finding{
			ID:         r.ID() + "." + sanitizeID(file),
			Title:      "Certificate validity warning detected",
			Severity:   SeverityWarn,
			Confidence: 0.82,
			Summary:    "The snapshot includes text indicating a certificate may be expired, not yet valid, or nearing expiry.",
			Hypothesis: "mTLS, xDS, or gateway connections may fail because of certificate validity issues or clock skew.",
			Evidence: []Evidence{
				{
					File:    file,
					Snippet: snippetAround(content, loc[0], loc[1], 180),
				},
			},
			RecommendedActions: []string{
				"Inspect certificate expiry dates and issuing CA.",
				"Verify node and container clocks are synchronized.",
				"Confirm auto-encrypt or certificate rotation completed successfully.",
			},
			Tags: []string{"tls", "certificates"},
		})
	}

	return findings
}

type MissingExpectedArtifactRule struct{}

func (r MissingExpectedArtifactRule) ID() string { return "bundle.missing_expected_artifact" }
func (r MissingExpectedArtifactRule) Evaluate(b *AnalyzeBundle) []Finding {
	expected := []string{
		"stats.json",
		"config_dump.json",
		"clusters.json",
		"listeners.json",
	}

	var missing []string
	for _, f := range expected {
		if _, ok := b.Files[f]; !ok {
			missing = append(missing, f)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	return []Finding{
		{
			ID:         r.ID(),
			Title:      "Snapshot is missing expected Envoy artifacts",
			Severity:   SeverityWarn,
			Confidence: 0.99,
			Summary:    "The analyzer expected standard Envoy admin output files that were not present in the bundle.",
			Hypothesis: "Capture may have partially failed, or the target pod did not expose all requested admin endpoints.",
			Evidence: []Evidence{
				{
					File:    "bundle",
					Snippet: "missing: " + strings.Join(missing, ", "),
				},
			},
			RecommendedActions: []string{
				"Re-run capture and confirm Envoy admin endpoint access.",
				"Check whether port-forward and ephemeral curl fallback both succeeded.",
				"Verify the target pod is the correct dataplane or gateway container.",
			},
			Tags: []string{"bundle", "capture", "envoy"},
		},
	}
}

type ClusterSemanticRule struct{}

func (r ClusterSemanticRule) ID() string { return "envoy.cluster.semantic" }
func (r ClusterSemanticRule) Evaluate(b *AnalyzeBundle) []Finding {
	content, ok := b.Files["clusters.json"]
	if !ok {
		return nil
	}

	clusters := parseClustersText(content)
	if len(clusters) == 0 {
		return []Finding{
			{
				ID:         r.ID() + ".parse_failed",
				Title:      "clusters.json could not be meaningfully parsed",
				Severity:   SeverityWarn,
				Confidence: 0.75,
				Summary:    "The clusters artifact was present, but the analyzer could not derive cluster state from it.",
				Hypothesis: "The clusters output format may differ from the current parser assumptions.",
				Evidence: []Evidence{
					{
						File:    "clusters.json",
						Snippet: snippetAround(content, 0, min(300, len(content)), 0),
					},
				},
				RecommendedActions: []string{
					"Inspect the raw clusters.json format and update the parser if needed.",
				},
				Tags: []string{"envoy", "clusters", "parser"},
			},
		}
	}

	var findings []Finding

	for _, clusterName := range sortedClusterNames(clusters) {
		cluster := clusters[clusterName]

		// Case 1: cluster has only unhealthy endpoints
		if len(cluster.Endpoints) > 0 {
			total := 0
			unhealthy := 0
			connectFails := 0
			rqErrors := 0
			rqTimeouts := 0
			noTraffic := 0

			for _, host := range cluster.Endpoints {
				total++

				if host.HealthFlags != "" && strings.ToLower(host.HealthFlags) != "healthy" {
					unhealthy++
				}

				if parseIntStat(host.Stats["cx_connect_fail"]) > 0 {
					connectFails++
				}
				if parseIntStat(host.Stats["rq_error"]) > 0 {
					rqErrors++
				}
				if parseIntStat(host.Stats["rq_timeout"]) > 0 {
					rqTimeouts++
				}

				if parseIntStat(host.Stats["cx_total"]) == 0 &&
					parseIntStat(host.Stats["rq_total"]) == 0 &&
					parseIntStat(host.Stats["cx_active"]) == 0 &&
					parseIntStat(host.Stats["rq_active"]) == 0 {
					noTraffic++
				}
			}

			if unhealthy == total && total > 0 {
				findings = append(findings, Finding{
					ID:         r.ID() + "." + sanitizeID(cluster.Name) + ".all_unhealthy",
					Title:      fmt.Sprintf("Cluster %s has no healthy endpoints", cluster.Name),
					Severity:   SeverityCritical,
					Confidence: 0.96,
					Summary:    "All parsed endpoints in this Envoy cluster appear unhealthy.",
					Hypothesis: "Routing to this cluster is likely failing because endpoint health is degraded or endpoints are stale.",
					Evidence: []Evidence{
						{
							File:    "clusters.json",
							Pointer: "cluster=" + cluster.Name,
							Snippet: buildClusterEvidenceSnippet(cluster),
						},
					},
					RecommendedActions: []string{
						"Check service registration and endpoint health for this cluster.",
						"Inspect readiness, proxy health checks, and upstream reachability.",
						"Compare against config_dump.json to confirm the cluster is still referenced by active routes.",
					},
					Tags: []string{"envoy", "clusters", "health"},
				})
			}

			if connectFails > 0 {
				findings = append(findings, Finding{
					ID:         r.ID() + "." + sanitizeID(cluster.Name) + ".connect_failures",
					Title:      fmt.Sprintf("Cluster %s shows endpoint connect failures", cluster.Name),
					Severity:   SeverityWarn,
					Confidence: 0.88,
					Summary:    "One or more endpoints in this cluster have non-zero connection failures.",
					Hypothesis: "Envoy is able to discover the cluster, but cannot consistently establish upstream connections.",
					Evidence: []Evidence{
						{
							File:    "clusters.json",
							Pointer: "cluster=" + cluster.Name,
							Snippet: buildClusterEvidenceSnippet(cluster),
						},
					},
					RecommendedActions: []string{
						"Check upstream listener reachability and service ports.",
						"Inspect TLS settings, destination addresses, and service mesh intentions.",
					},
					Tags: []string{"envoy", "clusters", "connectivity"},
				})
			}

			if rqErrors > 0 || rqTimeouts > 0 {
				findings = append(findings, Finding{
					ID:         r.ID() + "." + sanitizeID(cluster.Name) + ".request_errors",
					Title:      fmt.Sprintf("Cluster %s shows upstream request failures", cluster.Name),
					Severity:   SeverityWarn,
					Confidence: 0.85,
					Summary:    "One or more endpoints in this cluster have request errors or timeouts.",
					Hypothesis: "Traffic reaches the upstream, but requests are failing due to app errors, timeout behavior, or transport issues.",
					Evidence: []Evidence{
						{
							File:    "clusters.json",
							Pointer: "cluster=" + cluster.Name,
							Snippet: buildClusterEvidenceSnippet(cluster),
						},
					},
					RecommendedActions: []string{
						"Inspect app container logs and upstream response behavior.",
						"Review request timeout settings in ServiceDefaults, RouteTimeoutFilter, or HTTPRoute.",
					},
					Tags: []string{"envoy", "clusters", "upstream"},
				})
			}

			// Only warn on idle clusters when they are not obvious self/admin/local support clusters.
			if total > 0 && noTraffic == total && isPotentiallyImportantCluster(cluster.Name) {
				findings = append(findings, Finding{
					ID:         r.ID() + "." + sanitizeID(cluster.Name) + ".idle",
					Title:      fmt.Sprintf("Cluster %s has no observed traffic", cluster.Name),
					Severity:   SeverityInfo,
					Confidence: 0.62,
					Summary:    "All endpoints for this cluster appear idle during the capture window.",
					Hypothesis: "This may be normal if no traffic hit the route during capture, but it can also indicate unused or unreachable upstreams.",
					Evidence: []Evidence{
						{
							File:    "clusters.json",
							Pointer: "cluster=" + cluster.Name,
							Snippet: buildClusterEvidenceSnippet(cluster),
						},
					},
					RecommendedActions: []string{
						"Correlate with the time window of the capture and whether traffic was actively generated.",
						"Compare with stats.json and request logs to determine if the route was exercised.",
					},
					Tags: []string{"envoy", "clusters", "traffic"},
				})
			}
		}

		// Do not flag added_via_api=false alone. Only surface it as informational context if cluster is also suspicious.
		if cluster.AddedViaAPI != nil && !*cluster.AddedViaAPI && isPotentiallyImportantCluster(cluster.Name) {
			suspicious := clusterHasSuspiciousState(cluster)
			if suspicious {
				findings = append(findings, Finding{
					ID:         r.ID() + "." + sanitizeID(cluster.Name) + ".static_context",
					Title:      fmt.Sprintf("Cluster %s appears static while also showing anomalies", cluster.Name),
					Severity:   SeverityInfo,
					Confidence: 0.60,
					Summary:    "This cluster was not marked as dynamically added via API and also shows signs of unhealthy or failing behavior.",
					Hypothesis: "The cluster state may be useful context when comparing static/admin clusters versus dynamically delivered xDS resources.",
					Evidence: []Evidence{
						{
							File:    "clusters.json",
							Pointer: "cluster=" + cluster.Name,
							Snippet: buildClusterEvidenceSnippet(cluster),
						},
					},
					RecommendedActions: []string{
						"Use this as context only; do not treat added_via_api=false by itself as a failure.",
						"Compare the cluster against config_dump.json and active route references.",
					},
					Tags: []string{"envoy", "clusters", "context"},
				})
			}
		}
	}

	return findings
}

func grepLogsFinding(b *AnalyzeBundle, baseID, pattern, title string, severity Severity, confidence float64, hypothesis string, actions, tags []string) []Finding {
	re := regexp.MustCompile(pattern)
	var findings []Finding

	for file, content := range b.Logs {
		loc := re.FindStringIndex(content)
		if loc == nil {
			continue
		}

		findings = append(findings, Finding{
			ID:         baseID + "." + sanitizeID(file),
			Title:      title,
			Severity:   severity,
			Confidence: confidence,
			Summary:    "Matched rule pattern in captured logs.",
			Hypothesis: hypothesis,
			Evidence: []Evidence{
				{
					File:    file,
					Snippet: snippetAround(content, loc[0], loc[1], 220),
				},
			},
			RecommendedActions: actions,
			Tags:               tags,
		})
	}

	return findings
}

func sanitizeID(v string) string {
	v = strings.ReplaceAll(v, "/", ".")
	v = strings.ReplaceAll(v, "\\", ".")
	v = strings.ReplaceAll(v, " ", "_")
	v = strings.ReplaceAll(v, ":", "_")
	return v
}

func snippetAround(content string, start, end, width int) string {
	lo := start - width
	if lo < 0 {
		lo = 0
	}
	hi := end + width
	if hi > len(content) {
		hi = len(content)
	}
	snippet := content[lo:hi]
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.TrimSpace(snippet)
	if len(snippet) > 500 {
		snippet = snippet[:500]
	}
	return snippet
}

func parseClustersText(content string) map[string]*ParsedCluster {
	clusters := make(map[string]*ParsedCluster)

	lines := strings.Split(content, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		parts := strings.Split(line, "::")
		if len(parts) < 3 {
			continue
		}

		clusterName := parts[0]
		cluster, ok := clusters[clusterName]
		if !ok {
			cluster = &ParsedCluster{
				Name:            clusterName,
				DefaultPriority: map[string]string{},
				HighPriority:    map[string]string{},
				Endpoints:       map[string]*ClusterHost{},
			}
			clusters[clusterName] = cluster
		}

		// cluster::observability_name::foo
		if len(parts) == 3 {
			key := parts[1]
			val := parts[2]

			switch key {
			case "observability_name":
				cluster.ObservabilityName = val
			case "added_via_api":
				if val == "true" {
					b := true
					cluster.AddedViaAPI = &b
				} else if val == "false" {
					b := false
					cluster.AddedViaAPI = &b
				}
			}
			continue
		}

		// cluster::default_priority::max_connections::1024
		if len(parts) == 4 && (parts[1] == "default_priority" || parts[1] == "high_priority") {
			priorityName := parts[1]
			key := parts[2]
			val := parts[3]

			if priorityName == "default_priority" {
				cluster.DefaultPriority[key] = val
			} else {
				cluster.HighPriority[key] = val
			}
			continue
		}

		// cluster::127.0.0.1:20100::cx_active::0
		if len(parts) == 4 {
			addr := parts[1]
			key := parts[2]
			val := parts[3]

			host, ok := cluster.Endpoints[addr]
			if !ok {
				host = &ClusterHost{
					Address: addr,
					Stats:   map[string]string{},
				}
				cluster.Endpoints[addr] = host
			}

			host.Stats[key] = val

			switch key {
			case "health_flags":
				host.HealthFlags = val
			case "hostname":
				host.Hostname = val
			case "weight":
				host.Weight = val
			case "priority":
				host.Priority = val
			case "region":
				host.Region = val
			case "zone":
				host.Zone = val
			case "sub_zone":
				host.SubZone = val
			case "canary":
				host.Canary = val
			case "success_rate":
				host.SuccessRate = val
			case "local_origin_success_rate":
				host.LocalOriginSuccessRate = val
			}
		}
	}

	return clusters
}

func sortedClusterNames(clusters map[string]*ParsedCluster) []string {
	names := make([]string, 0, len(clusters))
	for name := range clusters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func parseIntStat(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

func clusterHasSuspiciousState(cluster *ParsedCluster) bool {
	for _, host := range cluster.Endpoints {
		if host.HealthFlags != "" && strings.ToLower(host.HealthFlags) != "healthy" {
			return true
		}
		if parseIntStat(host.Stats["cx_connect_fail"]) > 0 {
			return true
		}
		if parseIntStat(host.Stats["rq_error"]) > 0 {
			return true
		}
		if parseIntStat(host.Stats["rq_timeout"]) > 0 {
			return true
		}
	}
	return false
}

func isPotentiallyImportantCluster(name string) bool {
	lower := strings.ToLower(name)

	ignorePrefixes := []string{
		"self_admin",
		"prometheus_backend",
		"agent",
		"xds_cluster",
		"sds-grpc",
		"stats_sink",
	}

	for _, prefix := range ignorePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}

	return true
}

func buildClusterEvidenceSnippet(cluster *ParsedCluster) string {
	var parts []string
	parts = append(parts, "cluster="+cluster.Name)

	if cluster.AddedViaAPI != nil {
		parts = append(parts, fmt.Sprintf("added_via_api=%t", *cluster.AddedViaAPI))
	}

	for _, addr := range sortedHostAddrs(cluster.Endpoints) {
		host := cluster.Endpoints[addr]
		parts = append(parts,
			fmt.Sprintf(
				"%s health=%s cx_total=%s cx_active=%s cx_connect_fail=%s rq_total=%s rq_active=%s rq_error=%s rq_timeout=%s",
				addr,
				valueOr(host.HealthFlags, "unknown"),
				valueOr(host.Stats["cx_total"], "0"),
				valueOr(host.Stats["cx_active"], "0"),
				valueOr(host.Stats["cx_connect_fail"], "0"),
				valueOr(host.Stats["rq_total"], "0"),
				valueOr(host.Stats["rq_active"], "0"),
				valueOr(host.Stats["rq_error"], "0"),
				valueOr(host.Stats["rq_timeout"], "0"),
			),
		)
	}

	return strings.Join(parts, " | ")
}

func sortedHostAddrs(hosts map[string]*ClusterHost) []string {
	addrs := make([]string, 0, len(hosts))
	for addr := range hosts {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	return addrs
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildGraph(b *AnalyzeBundle) *Graph {
	g := &Graph{
		Nodes: []GraphNode{},
		Edges: []GraphEdge{},
	}

	added := map[string]struct{}{}
	addNode := func(n GraphNode) {
		if _, ok := added[n.ID]; ok {
			return
		}
		g.Nodes = append(g.Nodes, n)
		added[n.ID] = struct{}{}
	}

	bundleID := "bundle:" + b.PodName
	addNode(GraphNode{
		ID:   bundleID,
		Kind: "bundle",
		Name: filepath.Base(b.BundlePath),
	})

	if b.PodName != "" {
		podID := "pod:" + b.PodName
		addNode(GraphNode{
			ID:   podID,
			Kind: "pod",
			Name: b.PodName,
		})
		g.Edges = append(g.Edges, GraphEdge{From: bundleID, To: podID, Kind: "captures"})
	}

	for file := range b.Files {
		fileID := "file:" + file
		addNode(GraphNode{
			ID:   fileID,
			Kind: "artifact",
			Name: file,
		})
		g.Edges = append(g.Edges, GraphEdge{From: bundleID, To: fileID, Kind: "contains"})

		switch {
		case strings.HasSuffix(file, "-logs.txt"):
			container := strings.TrimSuffix(filepath.Base(file), "-logs.txt")
			containerID := "container:" + container
			addNode(GraphNode{
				ID:   containerID,
				Kind: "container",
				Name: container,
			})
			g.Edges = append(g.Edges, GraphEdge{From: containerID, To: fileID, Kind: "emits_logs"})
			if b.PodName != "" {
				g.Edges = append(g.Edges, GraphEdge{From: "pod:" + b.PodName, To: containerID, Kind: "runs"})
			}
		case file == "stats.json" || file == "config_dump.json" || file == "listeners.json" || file == "clusters.json" || file == "certs.json":
			adminID := "envoy-admin:" + file
			addNode(GraphNode{
				ID:   adminID,
				Kind: "envoy_admin_artifact",
				Name: file,
			})
			g.Edges = append(g.Edges, GraphEdge{From: adminID, To: fileID, Kind: "materialized_as"})
		case file == "xdsnap.pcap":
			pcapID := "pcap:" + b.PodName
			addNode(GraphNode{
				ID:   pcapID,
				Kind: "pcap",
				Name: "xdsnap.pcap",
			})
			g.Edges = append(g.Edges, GraphEdge{From: pcapID, To: fileID, Kind: "materialized_as"})
		}
	}

	// Enrich graph with parsed clusters if available
	if content, ok := b.Files["clusters.json"]; ok {
		clusters := parseClustersText(content)
		for _, clusterName := range sortedClusterNames(clusters) {
			cluster := clusters[clusterName]
			clusterID := "cluster:" + cluster.Name
			addNode(GraphNode{
				ID:   clusterID,
				Kind: "envoy_cluster",
				Name: cluster.Name,
				Metadata: map[string]string{
					"observability_name": cluster.ObservabilityName,
				},
			})
			g.Edges = append(g.Edges, GraphEdge{From: "file:clusters.json", To: clusterID, Kind: "describes"})

			for _, addr := range sortedHostAddrs(cluster.Endpoints) {
				host := cluster.Endpoints[addr]
				hostID := "endpoint:" + cluster.Name + ":" + addr
				addNode(GraphNode{
					ID:   hostID,
					Kind: "cluster_endpoint",
					Name: addr,
					Metadata: map[string]string{
						"health_flags": valueOr(host.HealthFlags, "unknown"),
						"rq_total":     valueOr(host.Stats["rq_total"], "0"),
						"cx_total":     valueOr(host.Stats["cx_total"], "0"),
					},
				})
				g.Edges = append(g.Edges, GraphEdge{From: clusterID, To: hostID, Kind: "has_endpoint"})
			}
		}
	}

	return g
}

func buildReport(bundle *AnalyzeBundle, findings []Finding, graph *Graph) *AnalyzeReport {
	summary := ReportSummary{}
	for _, f := range findings {
		summary.TotalFindings++
		switch f.Severity {
		case SeverityCritical:
			summary.CriticalFindings++
		case SeverityWarn:
			summary.WarningFindings++
		default:
			summary.InfoFindings++
		}
	}

	return &AnalyzeReport{
		Version:       "v1alpha1",
		GeneratedAt:   time.Now().UTC(),
		BundlePath:    bundle.BundlePath,
		PodName:       bundle.PodName,
		GraphIncluded: graph != nil,
		Summary:       summary,
		Findings:      findings,
	}
}

func renderMarkdown(report *AnalyzeReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# xDSnap Analysis Report\n\n")
	fmt.Fprintf(&b, "- Generated: %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Bundle: %s\n", report.BundlePath)
	if report.PodName != "" {
		fmt.Fprintf(&b, "- Pod: %s\n", report.PodName)
	}
	fmt.Fprintf(&b, "- Total findings: %d\n", report.Summary.TotalFindings)
	fmt.Fprintf(&b, "- Critical: %d\n", report.Summary.CriticalFindings)
	fmt.Fprintf(&b, "- Warnings: %d\n", report.Summary.WarningFindings)
	fmt.Fprintf(&b, "- Info: %d\n\n", report.Summary.InfoFindings)

	critical := filterSeverity(report.Findings, SeverityCritical)
	warnings := filterSeverity(report.Findings, SeverityWarn)
	infos := filterSeverity(report.Findings, SeverityInfo)

	if len(critical) > 0 {
		fmt.Fprintf(&b, "## Critical Findings\n\n")
		for _, f := range critical {
			renderFindingMarkdown(&b, f)
		}
	}
	if len(warnings) > 0 {
		fmt.Fprintf(&b, "## Warning Findings\n\n")
		for _, f := range warnings {
			renderFindingMarkdown(&b, f)
		}
	}
	if len(infos) > 0 {
		fmt.Fprintf(&b, "## Informational Findings\n\n")
		for _, f := range infos {
			renderFindingMarkdown(&b, f)
		}
	}
	if len(report.Findings) == 0 {
		fmt.Fprintf(&b, "No findings detected from the current offline ruleset.\n")
	}

	return b.String()
}

func renderFindingMarkdown(b *strings.Builder, f Finding) {
	fmt.Fprintf(b, "### %s\n\n", f.Title)
	fmt.Fprintf(b, "- ID: `%s`\n", f.ID)
	fmt.Fprintf(b, "- Severity: `%s`\n", f.Severity)
	fmt.Fprintf(b, "- Confidence: `%.2f`\n\n", f.Confidence)
	fmt.Fprintf(b, "%s\n\n", f.Summary)

	if f.Hypothesis != "" {
		fmt.Fprintf(b, "**Hypothesis**\n\n%s\n\n", f.Hypothesis)
	}

	if len(f.Evidence) > 0 {
		fmt.Fprintf(b, "**Evidence**\n\n")
		for _, ev := range f.Evidence {
			fmt.Fprintf(b, "- `%s`", ev.File)
			if ev.Pointer != "" {
				fmt.Fprintf(b, " (%s)", ev.Pointer)
			}
			if ev.Snippet != "" {
				fmt.Fprintf(b, ": `%s`", ev.Snippet)
			}
			fmt.Fprintf(b, "\n")
		}
		fmt.Fprintf(b, "\n")
	}

	if len(f.RecommendedActions) > 0 {
		fmt.Fprintf(b, "**Recommended actions**\n\n")
		for _, action := range f.RecommendedActions {
			fmt.Fprintf(b, "- %s\n", action)
		}
		fmt.Fprintf(b, "\n")
	}

	if len(f.Tags) > 0 {
		fmt.Fprintf(b, "Tags: `%s`\n\n", strings.Join(f.Tags, "`, `"))
	}
}

func filterSeverity(findings []Finding, severity Severity) []Finding {
	out := make([]Finding, 0)
	for _, f := range findings {
		if f.Severity == severity {
			out = append(out, f)
		}
	}
	return out
}

func writeFindingsJSONL(path string, findings []Finding) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, finding := range findings {
		if err := enc.Encode(finding); err != nil {
			return err
		}
	}
	return w.Flush()
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
