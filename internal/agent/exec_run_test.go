package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/assistantorigin"
)

type testNonceStore struct {
	mu   sync.Mutex
	used map[string]bool
}

type stoppedExecClient struct {
	client.Client
	creates int
	deletes int
	session *computev1alpha.InstanceConsoleSession
}

func (c *stoppedExecClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	switch v := obj.(type) {
	case *authv1.SelfSubjectReview:
		v.Status.UserInfo.Username = "alice@example.com"
	case *computev1alpha.InstanceConsoleSession:
		c.creates++
		v.UID = "new-session-uid"
		c.session = v.DeepCopy()
	default:
		return errors.New("unexpected create")
	}
	return nil
}

func (c *stoppedExecClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("simulated status read failure")
}

func (c *stoppedExecClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	c.deletes++
	return nil
}

func TestPlanApprovalRunRejectsUnauthorizedAndCleansUp(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	reader := suggestFixture()
	reader.instances["api"][0].UID = "instance-uid"
	backend := &stoppedExecClient{}
	nonces := &testNonceStore{used: map[string]bool{}}
	makeDeps := func(project, bearer string) DepsFor {
		return func(context.Context) (ToolDeps, error) {
			return ToolDeps{Reader: reader, Namespace: testNamespace, ExecClient: backend,
				Project: project, BearerToken: bearer, ExecKey: key, ExecNonces: nonces}, nil
		}
	}
	_, planned, err := execPlanHandler(makeDeps("project-a", "user-token"))(context.Background(), nil,
		ExecPlanInput{Instance: testExecInstance, Container: testExecContainer, Command: []string{"cat", "/etc/os-release"}, TTL: "2m"})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Instance != testExecInstance || planned.TTL != "2m0s" || planned.AssistantTimeLimit != "50s" ||
		planned.ApprovalText != "Approve exec "+planned.PlanToken {
		t.Fatalf("bad plan: %+v", planned)
	}
	approval := approvalForTest(key, "project-a", sha256Of("user-token"), planned.PlanToken)
	for _, tc := range []struct{ project, bearer, approval, token string }{
		{"project-a", "user-token", "", planned.PlanToken},
		{"project-b", "user-token", approval, planned.PlanToken},
		{"project-a", "other-token", approval, planned.PlanToken},
		{"project-a", "user-token", approval, planned.PlanToken + "changed"},
	} {
		_, _, err := execRunHandler(makeDeps(tc.project, tc.bearer))(context.Background(), nil,
			ExecRunInput{PlanToken: tc.token, Approval: tc.approval})
		if err == nil {
			t.Fatalf("accepted unauthorized run: %+v", tc)
		}
	}
	changed, err := verifyExecPlan(key, planned.PlanToken)
	if err != nil {
		t.Fatal(err)
	}
	changed.Command = []string{"cat", "/etc/shadow"}
	changedToken, err := signExecPlan(key, changed)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = execRunHandler(makeDeps("project-a", "user-token"))(context.Background(), nil,
		ExecRunInput{PlanToken: changedToken, Approval: approval})
	if err == nil {
		t.Fatal("approval applied to changed command")
	}
	changed.ExpiresAt = time.Now().Add(-time.Second).Unix()
	expiredToken, err := signExecPlan(key, changed)
	if err != nil {
		t.Fatal(err)
	}
	expiredApproval := approvalForTest(key, "project-a", sha256Of("user-token"), expiredToken)
	_, _, err = execRunHandler(makeDeps("project-a", "user-token"))(context.Background(), nil,
		ExecRunInput{PlanToken: expiredToken, Approval: expiredApproval})
	if err == nil {
		t.Fatal("expired plan ran")
	}
	if backend.creates != 0 {
		t.Fatal("unauthorized run created a session")
	}
	_, out, err := execRunHandler(makeDeps("project-a", "user-token"))(context.Background(), nil,
		ExecRunInput{PlanToken: planned.PlanToken, Approval: approval})
	if err != nil {
		t.Fatal(err)
	}
	if backend.creates != 1 || backend.deletes != 1 || out.Session == "" || !strings.Contains(out.Reason, "status read failure") {
		t.Fatalf("run/cleanup = creates:%d deletes:%d out:%+v", backend.creates, backend.deletes, out)
	}
	proof := backend.session.Annotations[computev1alpha.InstanceConsoleSessionAssistantProofAnnotation]
	if !assistantorigin.Verify(key, backend.session, "alice@example.com", proof, time.Now()) {
		t.Fatal("created session lacked a valid origin proof")
	}
	_, _, err = execRunHandler(makeDeps("project-a", "user-token"))(context.Background(), nil,
		ExecRunInput{PlanToken: planned.PlanToken, Approval: approval})
	if err == nil || backend.creates != 1 {
		t.Fatalf("replay reached API: %v, creates=%d", err, backend.creates)
	}
}

func (s *testNonceStore) Consume(_ context.Context, nonce string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used[nonce] {
		return false, nil
	}
	s.used[nonce] = true
	return true, nil
}

func TestExecPlanAndApprovalBindRequesterProjectTargetArgvAndTTL(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	plan := execPlan{Project: "project-a", RequesterHash: tokenHash("user-token"),
		Instance: "api-1", InstanceUID: types.UID("uid-1"), Container: "app",
		Command: []string{"cat", "/etc/os-release"}, TTLSeconds: 120,
		ExpiresAt: time.Now().Add(time.Minute).Unix(), Nonce: "1234567890abcdef"}
	token, err := signExecPlan(key, plan)
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifyExecPlan(key, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != plan.Project || got.InstanceUID != plan.InstanceUID ||
		got.TTLSeconds != plan.TTLSeconds || strings.Join(got.Command, " ") != strings.Join(plan.Command, " ") {
		t.Fatalf("plan changed after signing: %+v", got)
	}
	if _, err := verifyExecPlan(key, token+"tampered"); err == nil {
		t.Fatal("accepted changed plan")
	}
	if _, err := verifyExecPlan([]byte(strings.Repeat("x", 32)), token); err == nil {
		t.Fatal("accepted another signing key")
	}

	requesterHash := sha256Of("user-token")
	approval := approvalForTest(key, "project-a", requesterHash, token)
	if !validExecApproval(key, "project-a", "user-token", token, approval) {
		t.Fatal("refused valid approval")
	}
	for _, tc := range []struct{ project, bearer, token string }{
		{"project-b", "user-token", token}, {"project-a", "other-user", token}, {"project-a", "user-token", token + "changed"},
	} {
		if validExecApproval(key, tc.project, tc.bearer, tc.token, approval) {
			t.Fatalf("accepted wrong binding: %+v", tc)
		}
	}
	if validExecApproval(key, "project-a", "user-token", token, base64.RawURLEncoding.EncodeToString([]byte("forged"))) {
		t.Fatal("accepted forged approval")
	}
}

func TestExecPlanExpiryAndReplay(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	plan := execPlan{Project: "project", RequesterHash: tokenHash("token"), Instance: "i",
		InstanceUID: "uid", Container: "app", Command: []string{"true"}, TTLSeconds: 60,
		ExpiresAt: time.Now().Add(-time.Second).Unix(), Nonce: "nonce"}
	token, err := signExecPlan(key, plan)
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifyExecPlan(key, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt > time.Now().Unix() {
		t.Fatal("expected expired plan")
	}
	store := &testNonceStore{used: map[string]bool{}}
	if ok, _ := store.Consume(context.Background(), got.Nonce); !ok {
		t.Fatal("first consume failed")
	}
	if ok, _ := store.Consume(context.Background(), got.Nonce); ok {
		t.Fatal("replay succeeded")
	}
}

func TestExecOutputIsBounded(t *testing.T) {
	var out limitedOutput
	chunk := []byte(strings.Repeat("x", maxExecOutput+100))
	if n, err := out.Write(chunk); err != nil || n != len(chunk) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if out.Len() != maxExecOutput || !out.truncated {
		t.Fatalf("output = %d bytes, truncated=%t", out.Len(), out.truncated)
	}
}

func sha256Of(s string) [sha256.Size]byte { return sha256.Sum256([]byte(s)) }

func approvalForTest(key []byte, project string, requesterHash [sha256.Size]byte, token string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("assistant-exec-approval-v1\x00"))
	_, _ = mac.Write([]byte(project))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(requesterHash[:])
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
