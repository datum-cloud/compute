package agent

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	agentdocs "go.datum.net/compute/docs/agent"
)

// The copy in this package is read by a customer, not by the person who wrote
// the controllers. These tests pin the two halves of that: the words are the
// customer's, and the facts they need in order to escalate survive the
// translation.

// bannedTerm is one piece of vocabulary the customer has no way to know, with
// the reason it is banned, so a future reader can argue with the list rather
// than guess at it.
type bannedTerm struct {
	// pattern matches the term in prose. Case-insensitive, anchored on word
	// boundaries, and applied only after every API identifier has been removed
	// — an identifier quoted as evidence is allowed, the same word used as
	// English is not.
	pattern *regexp.Regexp
	// why justifies the ban.
	why string
}

func banned(pattern, why string) bannedTerm {
	return bannedTerm{pattern: regexp.MustCompile(`(?i)` + pattern), why: why}
}

// internalVocabulary is the denylist.
//
// The test is: does the word appear in the workload the customer authors
// (api/v1alpha), or in output they already read? Then it is theirs — image,
// replicas, ConfigMap, Secret, placement, network interface, scheduling gate,
// quota — and it survives. Does it appear only inside Datum's implementation?
// Then a customer reading it learns nothing, and it is banned here.
//
// Absence from Datum's public documentation was the first draft of this test
// and is a weaker one: it flagged ConfigMap, which is a field the customer
// types themselves and which the docs simply have no page for yet. Check the
// API, not the page count.
func internalVocabulary() []bannedTerm {
	return []bannedTerm{
		banned(`\bcells?\b`,
			"a Datum-internal unit of infrastructure; absent from the public docs, so a customer "+
				"reading it learns nothing. Say where the workload runs, or say Datum."),
		banned(`\ballowance\s?buckets?\b`,
			"the internal object that holds a project's quota. Customers cannot see one, create "+
				"one, or act on the word. Say the project's compute quota — they already meet "+
				"that word in QuotaExceeded."),
		banned(`\breconcil`,
			"controller-loop vocabulary. It describes how Datum works, never what happened to "+
				"the customer's workload."),
		banned(`\bcontrollers?\b`,
			"the reader does not know what one is. \"No controller reported success or failure\" "+
				"is the single sentence this rewrite exists to translate: say Datum has not "+
				"reported back either way."),
		banned(`\b(quota|resource)[\s-]?claims?\b`,
			"the internal request compute files against a project's quota. To a customer a claim "+
				"is something you file with an insurer. Say the check against the project's "+
				"compute quota.\n  note: the bare word is deliberately NOT banned — \"the status "+
				"claims the work is under way\" is ordinary English and is exactly how the copy "+
				"separates what was claimed from what was seen."),
		banned(`\bcontrol planes?\b`,
			"how Datum is built, not something a customer with a stuck workload reasons about. "+
				"The thing they own and can see is their project."),
		banned(`\bpods?\b`,
			"the runtime unit underneath an Instance. Customers deploy containers and instances "+
				"and never see a pod."),
		banned(`\bmilo\b`,
			"the name of an internal Datum service. It means nothing outside Datum."),
		banned(`\brbac\b`,
			"internal authorization vocabulary. Say Datum does not have permission to read it."),
		banned(`\badmission\b`,
			"the API-server extension point that rejected the request. The customer needs to "+
				"know they were rejected and by what, not which component did it."),
		banned(`\bbackends?\b`,
			"\"the quota backend\" is an implementation detail. Say the service that checks the "+
				"project's compute allowance, or just say Datum."),
		banned(`\bresource(claim|registration)s?\b`,
			"internal object kinds behind the quota check; never visible to a customer."),
		banned(`\bcompanions?\b`,
			"compute's internal name for a copy of a ConfigMap or Secret delivered alongside an "+
				"instance. The customer has one object and one name for it."),
		banned(`\bmaterialis|\bmaterializ`,
			"implementation vocabulary for the same delivery step. Say it arrived, or has not."),
		banned(`\bresolvers?\b`,
			"the component that reads the ConfigMaps and Secrets. Name the thing that is missing, "+
				"not the component that went looking."),
	}
}

// customerFacingOnly are banned in the copy that reaches a customer, but not in
// the runbooks and knowledge document, which are addressed to the assistant and
// have to name the machinery in order to tell it what to read.
func customerFacingOnly() []bannedTerm {
	return []bannedTerm{
		banned(`\bconditions?\b`,
			"the customer sees a workload that is not running, not a condition. The condition "+
				"type still travels as an identifier in parentheses; the sentence around it "+
				"must not lean on the word."),
		banned(`\bprogramm(ed|ing)\b`,
			"compute's word for building the machine behind an Instance. The reason string "+
				"ProgrammingInProgress survives as evidence; the prose says what it means."),
	}
}

// apiIdentifiers are the strings that may appear in copy verbatim: reason
// codes, condition types, skill and pattern names. They are evidence — a
// customer quotes them when escalating — so they are removed before the prose
// is scanned rather than being allowed to trip it.
func apiIdentifiers() []string {
	seen := map[string]struct{}{
		PatternNoTerminalStateReported: {},
		SkillWorkloadNotAvailable:      {},
		SkillQuotaTriage:               {},
		SkillInstanceNotReady:          {},
		SkillReferencedData:            {},
		SkillPlacementTriage:           {},
		SkillStalledTransient:          {},
	}
	for _, info := range AllReasons() {
		seen[info.Reason] = struct{}{}
		for _, ct := range info.ConditionTypes {
			seen[ct] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	// Longest first, so "InstanceProgrammed" is removed before "Programmed"
	// could match inside it and leave a fragment behind.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if len(out[j]) > len(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// prose strips the API identifiers, and any extra identifiers the caller
// supplies, out of a string — leaving the English around them.
//
// The extras matter: object names are chosen by the customer, and a workload
// legitimately called "api-backend" must not be read as prose that says
// "backend".
func prose(s string, extra ...string) string {
	for _, id := range append(apiIdentifiers(), extra...) {
		if id != "" {
			s = strings.ReplaceAll(s, id, " ")
		}
	}
	return s
}

// checkCopy scans one piece of prose against a denylist.
func checkCopy(t *testing.T, where, text string, terms []bannedTerm, extra ...string) {
	t.Helper()
	p := prose(text, extra...)
	for _, term := range terms {
		if m := term.pattern.FindString(p); m != "" {
			t.Errorf("%s uses %q, which a customer has no way to read.\n  why: %s\n  in: %s",
				where, m, term.why, strings.TrimSpace(text))
		}
	}
}

// TestCatalogCopyUsesNoInternalVocabulary covers the table the assistant reads
// out almost verbatim.
func TestCatalogCopyUsesNoInternalVocabulary(t *testing.T) {
	terms := append(internalVocabulary(), customerFacingOnly()...)
	for _, info := range AllReasons() {
		checkCopy(t, info.Reason+".Explanation", info.Explanation, terms)
		checkCopy(t, info.Reason+".Remediation", info.Remediation, terms)
	}
}

// TestDiagnosisCopyUsesNoInternalVocabulary covers the sentences the walk
// assembles at read time, which the catalog test cannot see: the stalled
// advice, the unreported framing, the pattern narrative, the summary and the
// next steps.
func TestDiagnosisCopyUsesNoInternalVocabulary(t *testing.T) {
	terms := append(internalVocabulary(), customerFacingOnly()...)
	for name, d := range diagnosisFixtures() {
		names := objectNames(d)
		checkCopy(t, name+".Summary", d.Summary, terms, names...)
		for _, c := range d.ContributingConditions {
			checkCopy(t, name+"."+c.Reason+".Explanation", c.Explanation, terms, names...)
			checkCopy(t, name+"."+c.Reason+".Remediation", c.Remediation, terms, names...)
		}
		for i, s := range d.NextSteps {
			checkCopy(t, name+".NextSteps["+itoa(i)+"]", s, terms, names...)
		}
	}
}

// TestPublishedDocsUseNoInternalVocabulary covers the knowledge document and
// the runbooks. These are addressed to the assistant rather than the customer,
// so they may say "condition" — but they are also where the assistant learns
// its vocabulary, and a runbook that says "cell" produces an answer that says
// "cell".
func TestPublishedDocsUseNoInternalVocabulary(t *testing.T) {
	terms := internalVocabulary()
	entries, err := fs.ReadDir(agentdocs.FS, agentdocs.SkillsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", agentdocs.SkillsDir, err)
	}
	files := make([]string, 0, 1+len(entries))
	files = append(files, agentdocs.KnowledgeFile)
	for _, e := range entries {
		files = append(files, path.Join(agentdocs.SkillsDir, e.Name()))
	}
	if len(files) < 7 {
		t.Fatalf("found %d published documents, want the knowledge document and six skills", len(files))
	}
	for _, name := range files {
		b, err := fs.ReadFile(agentdocs.FS, name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			checkCopy(t, name+":"+itoa(i+1), line, terms)
		}
	}
}

// TestPlainLanguageKeepsTheEvidence is the counterweight. Every identifier a
// customer needs in order to escalate has to survive the rewrite: plain
// language is not the same as vague language, and a summary that reads well but
// names nothing is worse than the jargon it replaced.
func TestPlainLanguageKeepsTheEvidence(t *testing.T) {
	created := stagingNow.Add(-9 * 24 * time.Hour)
	rewritten := stagingNow.Add(-9*time.Hour - 30*time.Minute)

	w := workloadCreated("joseszycho-billing-test", created,
		cond(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments."))
	insts := []computev1alpha.Instance{
		instanceCreated("joseszycho-billing-test-dfw-dfw-0", created,
			condAt(computev1alpha.InstanceProgrammed, "Unknown",
				computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
				"Instance is provisioning", rewritten)),
	}
	d := DiagnoseAt(stagingNow, w, nil, insts)

	// The summary is the one line most likely to be quoted whole, so it carries
	// the object, the reason code, the condition type and both clocks.
	for _, want := range []string{
		"joseszycho-billing-test-dfw-dfw-0",
		computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
		computev1alpha.InstanceProgrammed,
		stagingInState,
		"9d",
	} {
		if !strings.Contains(d.Summary, want) {
			t.Errorf("Summary = %q, want it to keep the evidence %q", d.Summary, want)
		}
	}
	// And the absolute timestamp survives on the cause, for an escalation that
	// outlives the conversation.
	if d.RootCause.LastTransitionTime == "" {
		t.Error("LastTransitionTime is empty; the exact timestamp is evidence and must survive")
	}
}

// objectNames collects every name the customer chose, so the scan reads them as
// the evidence they are rather than as words.
func objectNames(d Diagnosis) []string {
	names := make([]string, 0, 1+len(d.ContributingConditions)+len(d.Instances.Blocked))
	names = append(names, d.Workload)
	for _, c := range d.ContributingConditions {
		names = append(names, c.Object)
	}
	for _, b := range d.Instances.Blocked {
		names = append(names, b.Name)
	}
	return names
}

// diagnosisFixtures exercises every branch that assembles prose at read time,
// keyed by what each one covers.
func diagnosisFixtures() map[string]Diagnosis {
	created := stagingNow.Add(-9 * 24 * time.Hour)
	fresh := stagingNow.Add(-2 * time.Minute)
	stale := stagingNow.Add(-5 * 24 * time.Hour)

	out := map[string]Diagnosis{}

	out["healthy"] = DiagnoseAt(stagingNow,
		workload("web-frontend", 3, 3,
			cond(computev1alpha.WorkloadAvailable, "True",
				computev1alpha.WorkloadDeploymentReasonStableInstanceFound, "Serving.")),
		nil, []computev1alpha.Instance{
			instance("web-frontend-a-1", cond(computev1alpha.InstanceReady, "True",
				computev1alpha.InstanceReadyReasonAvailable, "Serving.")),
		})

	out["no-root-cause"] = DiagnoseAt(stagingNow, workload("silent", 0, 2), nil, nil)

	out["user-actionable"] = DiagnoseAt(stagingNow,
		workload("api-backend", 2, 6,
			cond(computev1alpha.WorkloadAvailable, "False",
				computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, "Quota is blocking 4 of 6.")),
		nil, []computev1alpha.Instance{
			instance("api-backend-a-e5f6",
				condAt(computev1alpha.InstanceQuotaGranted, "False",
					computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
					"Requested 4 vCPU; 2 vCPU remaining.", fresh)),
		})

	out["platform-fault"] = DiagnoseAt(stagingNow,
		workload("edge-cache", 0, 4,
			cond(computev1alpha.WorkloadAvailable, "False",
				computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments.")),
		[]computev1alpha.WorkloadDeployment{
			deployment("edge-cache-ams",
				condAt(computev1alpha.WorkloadDeploymentAvailable, "False",
					computev1alpha.WorkloadDeploymentReasonCityCodeMismatch,
					"Asked for AMS; serving LHR.", fresh)),
		}, nil)

	out["transient"] = DiagnoseAt(stagingNow,
		workload("rolling-out", 0, 1,
			condAt(computev1alpha.WorkloadAvailable, "False",
				computev1alpha.InstanceProgrammedReasonProgrammingInProgress, "Building.", fresh)),
		nil, nil)

	// Stalled with a reported failure, and stalled with nothing reported: the
	// two say different things about what is known, so both are scanned.
	out["stalled-reported"] = DiagnoseAt(stagingNow,
		workload("xcheck-iad", 0, 1,
			condAt(computev1alpha.InstanceProgrammed, "False",
				computev1alpha.InstanceProgrammedReasonProgrammingInProgress, "Building.", stale)),
		nil, nil)

	out["stalled-unreported"] = DiagnoseAt(stagingNow,
		workload("xcheck-iad", 0, 1,
			condAt(computev1alpha.InstanceProgrammed, "Unknown",
				computev1alpha.InstanceProgrammedReasonProgrammingInProgress, "Building.", stale)),
		nil, nil)

	// The staging shape, at both levels: the instance branch names the crash
	// loop, the workload branch rules nothing out.
	out["pattern-instance"] = DiagnoseAt(stagingNow,
		workloadCreated("demo2loc", created,
			cond(computev1alpha.WorkloadAvailable, "False",
				computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments.")),
		nil, []computev1alpha.Instance{
			instanceCreated("demo2loc-dfw-dfw-0", created,
				condAt(computev1alpha.InstanceProgrammed, "Unknown",
					computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
					"Instance is provisioning", stagingNow.Add(-9*time.Hour))),
		})

	out["pattern-workload"] = DiagnoseAt(stagingNow,
		workloadCreated("demo2loc", created,
			condAt(computev1alpha.WorkloadAvailable, "Unknown",
				computev1alpha.WorkloadDeploymentReasonNetworkProvisioning,
				"Provisioning network.", stagingNow.Add(-9*time.Hour))),
		nil, nil)

	// A discarded timestamp, and a reason nothing in the catalog covers.
	out["epoch-sentinel"] = DiagnoseAt(stagingNow,
		workloadCreated("demo2loc", created,
			cond(computev1alpha.WorkloadAvailable, "False",
				computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments.")),
		nil, []computev1alpha.Instance{
			instanceCreated("demo2loc-dfw-dfw-1", created,
				epochCond(computev1alpha.InstanceProgrammed, "Unknown",
					computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
					"Instance is provisioning")),
		})

	out["uncatalogued"] = DiagnoseAt(stagingNow,
		workload("mystery", 0, 1,
			cond(computev1alpha.WorkloadAvailable, "False", "SomeBrandNewReason", "Something new.")),
		nil, nil)

	out["suspended"] = DiagnoseAt(stagingNow,
		workload("held", 0, 1,
			cond(computev1alpha.WorkloadAvailable, "False",
				computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments.")),
		nil, []computev1alpha.Instance{
			instance("held-0", condAt(computev1alpha.InstanceReady, "False",
				computev1alpha.InstanceReadyReasonSuspended, "Project suspended.", fresh)),
		})

	return out
}

// itoa keeps the failure labels readable without pulling in strconv for one
// call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
