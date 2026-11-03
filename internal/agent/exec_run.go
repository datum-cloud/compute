package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/assistantorigin"
	"go.datum.net/compute/internal/consoleclient"
	"go.datum.net/compute/internal/consolesession"
)

const (
	ToolExecPlan  = "compute_exec_plan"
	ToolExecRun   = "compute_exec_run"
	planLifetime  = 5 * time.Minute
	maxExecWait   = 50 * time.Second
	maxExecOutput = 32 << 10
)

// ExecNonceStore atomically consumes a random plan nonce across MCP replicas.
// Without a shared store, the execution tool refuses to run.
type ExecNonceStore interface {
	Consume(ctx context.Context, nonce string) (bool, error)
}

type ExecPlanInput = ExecSuggestInput

type ExecPlanOutput struct {
	Instance           string   `json:"instance"`
	Container          string   `json:"container"`
	Command            []string `json:"command"`
	TTL                string   `json:"ttl"`
	AssistantTimeLimit string   `json:"assistantTimeLimit"`
	PlanToken          string   `json:"planToken"`
	ExpiresAt          string   `json:"expiresAt"`
	ApprovalText       string   `json:"approvalText"`
	Note               string   `json:"note"`
}

type ExecRunInput struct {
	PlanToken string `json:"planToken" jsonschema:"The exact opaque token returned by compute_exec_plan."`
	Approval  string `json:"approval,omitempty" jsonschema:"Server-generated approval assertion; the assistant runtime supplies this after an exact current user approval."`
}

type ExecRunOutput struct {
	Instance        string `json:"instance"`
	Container       string `json:"container"`
	Session         string `json:"session"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	OutputTruncated bool   `json:"outputTruncated"`
	ExitCode        int    `json:"exitCode"`
	Reason          string `json:"reason,omitempty"`
	TimedOut        bool   `json:"timedOut,omitempty"`
}

type execPlan struct {
	Project       string    `json:"project"`
	RequesterHash string    `json:"requesterHash"`
	Instance      string    `json:"instance"`
	InstanceUID   types.UID `json:"instanceUID"`
	Container     string    `json:"container"`
	Command       []string  `json:"command"`
	TTLSeconds    int64     `json:"ttlSeconds"`
	ExpiresAt     int64     `json:"expiresAt"`
	Nonce         string    `json:"nonce"`
}

// RegisterExecTools adds the opt-in plan/run path. The caller must enable it
// only after installing a shared signing key and atomic nonce store.
func RegisterExecTools(s *mcp.Server, deps DepsFor) {
	mcp.AddTool(s, &mcp.Tool{Name: ToolExecPlan, Annotations: readOnly(),
		Title: "Plan an instance command",
		Description: "Prepare the exact Instance, container, argument vector and TTL for one command. " +
			"Creates no session. Show the whole plan and approval text to the person; a later user turn must contain exactly that approval text."}, execPlanHandler(deps))
	mutating := false
	mcp.AddTool(s, &mcp.Tool{Name: ToolExecRun,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &mutating, IdempotentHint: false},
		Title:       "Run one approved instance command",
		Description: "Run one command only after the assistant runtime verifies the current user's exact approval text. " +
			"A server approval assertion and single-use nonce are mandatory; the model cannot authorize this call."}, execRunHandler(deps))
}

func execPlanHandler(deps DepsFor) mcp.ToolHandlerFor[ExecPlanInput, ExecPlanOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ExecPlanInput) (*mcp.CallToolResult, ExecPlanOutput, error) {
		var out ExecPlanOutput
		if in.Instance == "" {
			return nil, out, errors.New("name the Instance to check")
		}
		if err := validateExecArgs(in.Command); err != nil {
			return nil, out, err
		}
		ttl := 15 * time.Minute
		if in.TTL != "" {
			parsed, err := time.ParseDuration(in.TTL)
			if err != nil || parsed < time.Minute || parsed > time.Hour || parsed%time.Second != 0 {
				return nil, out, errors.New("ttl must be whole seconds from 1m to 1h")
			}
			ttl = parsed
		}
		d, err := deps(ctx)
		if err != nil {
			return nil, out, err
		}
		if d.Reader == nil || len(d.ExecKey) < 32 || d.Project == "" || d.BearerToken == "" {
			return nil, out, errors.New("assistant execution planning is not configured")
		}
		inst, err := d.Reader.GetInstance(ctx, d.Namespace, in.Instance)
		if err != nil {
			return nil, out, err
		}
		container, err := execContainer(inst, in.Container)
		if err != nil {
			return nil, out, err
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return nil, out, err
		}
		plan := execPlan{Project: d.Project, RequesterHash: tokenHash(d.BearerToken),
			Instance: inst.Name, InstanceUID: inst.UID, Container: container,
			Command: append([]string(nil), in.Command...), TTLSeconds: int64(ttl.Seconds()),
			ExpiresAt: time.Now().Add(planLifetime).Unix(), Nonce: hex.EncodeToString(nonce)}
		token, err := signExecPlan(d.ExecKey, plan)
		if err != nil {
			return nil, out, err
		}
		if len(token) > 4096 {
			return nil, out, errors.New("assistant exec command is too long to approve in chat")
		}
		return nil, ExecPlanOutput{Instance: inst.Name, Container: container,
			Command: plan.Command, TTL: ttl.String(), AssistantTimeLimit: maxExecWait.String(), PlanToken: token,
			ExpiresAt:    time.Unix(plan.ExpiresAt, 0).UTC().Format(time.RFC3339),
			ApprovalText: "Approve exec " + token,
			Note:         "Nothing has run. Show the exact target, argv and TTL to the person. The assistant stops waiting and ends the command after 50 seconds even if the session TTL is longer. Only the person's next exact approval message can authorize execution."}, nil
	}
}

func execRunHandler(deps DepsFor) mcp.ToolHandlerFor[ExecRunInput, ExecRunOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ExecRunInput) (*mcp.CallToolResult, ExecRunOutput, error) {
		var out ExecRunOutput
		d, err := deps(ctx)
		if err != nil {
			return nil, out, err
		}
		if d.ExecClient == nil || d.ExecNonces == nil || len(d.ExecKey) < 32 || d.Project == "" || d.BearerToken == "" {
			return nil, out, errors.New("assistant execution is not configured")
		}
		plan, err := verifyExecPlan(d.ExecKey, in.PlanToken)
		if err != nil {
			return nil, out, err
		}
		if plan.Project != d.Project || plan.RequesterHash != tokenHash(d.BearerToken) {
			return nil, out, errors.New("exec plan belongs to another requester or project; plan again")
		}
		if plan.ExpiresAt <= time.Now().Unix() {
			return nil, out, errors.New("exec approval expired; plan again")
		}
		if !validExecApproval(d.ExecKey, d.Project, d.BearerToken, in.PlanToken, in.Approval) {
			return nil, out, errors.New("a verified current-user approval is required")
		}
		used, err := d.ExecNonces.Consume(ctx, plan.Nonce)
		if err != nil {
			return nil, out, fmt.Errorf("claiming exec approval: %w", err)
		}
		if !used {
			return nil, out, errors.New("this exec plan was already used")
		}
		return nil, runPlannedExec(ctx, d, plan), nil
	}
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func signExecPlan(key []byte, plan execPlan) (string, error) {
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("compute-exec-plan-v1\x00" + body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyExecPlan(key []byte, token string) (execPlan, error) {
	var plan execPlan
	if len(token) > 4096 {
		return plan, errors.New("invalid exec plan")
	}
	body, signature, ok := strings.Cut(token, ".")
	if !ok {
		return plan, errors.New("invalid exec plan")
	}
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return plan, errors.New("invalid exec plan")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("compute-exec-plan-v1\x00" + body))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return plan, errors.New("invalid exec plan signature")
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(data, &plan) != nil {
		return plan, errors.New("invalid exec plan")
	}
	if plan.Project == "" || plan.RequesterHash == "" || plan.Instance == "" || plan.InstanceUID == "" ||
		plan.Container == "" || plan.Nonce == "" || plan.TTLSeconds < 60 || plan.TTLSeconds > 3600 ||
		validateExecArgs(plan.Command) != nil {
		return execPlan{}, errors.New("invalid exec plan")
	}
	return plan, nil
}

func validExecApproval(key []byte, project, bearer, planToken, approval string) bool {
	requesterHash := sha256.Sum256([]byte(bearer))
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("assistant-exec-approval-v1\x00"))
	_, _ = mac.Write([]byte(project))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(requesterHash[:])
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(planToken))
	got, err := base64.RawURLEncoding.DecodeString(approval)
	return err == nil && hmac.Equal(got, mac.Sum(nil))
}

func execContainer(inst *computev1alpha.Instance, requested string) (string, error) {
	if inst.Spec.Runtime.Sandbox == nil || len(inst.Spec.Runtime.Sandbox.Containers) == 0 {
		return "", fmt.Errorf("instance %q has no container to run a command in", inst.Name)
	}
	containers := inst.Spec.Runtime.Sandbox.Containers
	if requested == "" && len(containers) == 1 {
		return containers[0].Name, nil
	}
	if requested == "" {
		return "", fmt.Errorf("instance %q has several containers; name one", inst.Name)
	}
	for _, c := range containers {
		if c.Name == requested {
			return requested, nil
		}
	}
	return "", fmt.Errorf("instance %q has no container %q", inst.Name, requested)
}

func runPlannedExec(ctx context.Context, d ToolDeps, plan execPlan) ExecRunOutput {
	out := ExecRunOutput{Instance: plan.Instance, Container: plan.Container, ExitCode: 1}
	ctx, cancel := context.WithTimeout(ctx, maxExecWait)
	defer cancel()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		out.Reason = "cannot create session key: " + err.Error()
		return out
	}
	defer clear(key)
	session := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: "assistant-exec-" + plan.Nonce},
		Spec: computev1alpha.InstanceConsoleSessionSpec{
			InstanceRef:   computev1alpha.InstanceConsoleSessionInstanceRef{Name: plan.Instance, UID: plan.InstanceUID},
			ContainerName: plan.Container, Command: plan.Command, ClientPublicKey: consolesession.PublicKey(key),
			TTL: &metav1.Duration{Duration: time.Duration(plan.TTLSeconds) * time.Second},
		},
	}
	// Ask the project control plane who the forwarded bearer token belongs to.
	// The origin proof must bind the same authenticated requester admission
	// will stamp on the session; failure to resolve identity stops the command.
	identity := &authv1.SelfSubjectReview{}
	if err := d.ExecClient.Create(ctx, identity); err != nil {
		out.Reason = "verifying requester identity: " + err.Error()
		return out
	}
	requester := identity.Status.UserInfo.Username
	if requester == "" {
		out.Reason = "the control plane did not return a requester identity"
		return out
	}
	proof, err := assistantorigin.Sign(d.ExecKey, session, requester, plan.Nonce, time.Now().Add(2*time.Minute))
	if err != nil {
		out.Reason = "signing assistant origin: " + err.Error()
		return out
	}
	session.Annotations = map[string]string{computev1alpha.InstanceConsoleSessionAssistantProofAnnotation: proof}
	if err := d.ExecClient.Create(ctx, session); err != nil {
		out.Reason = "creating session: " + err.Error()
		return out
	}
	out.Session = session.Name
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = client.IgnoreNotFound(d.ExecClient.Delete(cleanupCtx, session))
	}()
	for session.Status.Connection == nil {
		if err := d.ExecClient.Get(ctx, client.ObjectKeyFromObject(session), session); err != nil {
			out.Reason = "reading session: " + err.Error()
			return out
		}
		cond := meta.FindStatusCondition(session.Status.Conditions, computev1alpha.InstanceConsoleSessionReady)
		if cond != nil && cond.Status == metav1.ConditionFalse {
			out.Reason = cond.Reason + ": " + cond.Message
			return out
		}
		if ctx.Err() != nil {
			out.TimedOut = true
			out.Reason = "session did not become ready in time"
			return out
		}
		select {
		case <-ctx.Done():
			out.TimedOut = true
			out.Reason = "session did not become ready in time"
			return out
		case <-time.After(500 * time.Millisecond):
		}
	}
	conn := consoleclient.Connection{EndpointID: session.Status.Connection.EndpointID,
		RelayURLs: session.Status.Connection.RelayURLs, Target: session.Status.Connection.Target}
	stream, err := consoleclient.Connect(ctx, key, string(session.UID), conn)
	if err != nil {
		out.Reason = "connecting to session: " + err.Error()
		return out
	}
	defer func() { _ = stream.Close() }()
	go func() { <-ctx.Done(); _ = stream.Close() }()
	stdout, stderr := &limitedOutput{}, &limitedOutput{}
	res, err := stream.Wait(stdout, stderr)
	out.Stdout, out.Stderr = stdout.String(), stderr.String()
	out.OutputTruncated = stdout.truncated || stderr.truncated
	if ctx.Err() != nil {
		out.TimedOut = true
		out.Reason = "assistant execution time limit reached"
		return out
	}
	if err != nil {
		out.Reason = "session stream ended: " + err.Error()
		return out
	}
	out.ExitCode, out.Reason = res.ExitCode, res.Reason
	return out
}

type limitedOutput struct {
	bytes.Buffer
	truncated bool
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := maxExecOutput - w.Len()
	if left < n {
		w.truncated = true
		if left > 0 {
			_, _ = w.Buffer.Write(p[:left])
		}
		return n, nil
	}
	_, _ = io.Copy(&w.Buffer, bytes.NewReader(p))
	return n, nil
}
