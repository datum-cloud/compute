package assistantorigin

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestProofBindsRequesterAndWholeSpec(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	now := time.Now().Truncate(time.Second)
	session := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "assistant-exec-" + strings.Repeat("a", 32)},
		Spec: computev1alpha.InstanceConsoleSessionSpec{
			InstanceRef:   computev1alpha.InstanceConsoleSessionInstanceRef{Name: "api", UID: "uid-1"},
			ContainerName: "app", Command: []string{"cat", "/etc/os-release"},
			ClientPublicKey: strings.Repeat("a", 64),
			TTL:             &metav1.Duration{Duration: time.Minute},
		},
	}
	proof, err := Sign(key, session, "user@example.com", strings.Repeat("a", 32), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(key, session, "user@example.com", proof, now) {
		t.Fatal("valid proof rejected")
	}
	if Verify(key, session, "another-user", proof, now) {
		t.Fatal("wrong requester accepted")
	}
	if Verify(key, session, "user@example.com", proof, now.Add(3*time.Minute)) {
		t.Fatal("expired proof accepted")
	}
	if Verify([]byte(strings.Repeat("z", 32)), session, "user@example.com", proof, now) {
		t.Fatal("forged key accepted")
	}
	mutations := []func(*computev1alpha.InstanceConsoleSession){
		func(s *computev1alpha.InstanceConsoleSession) { s.Name = "copied-session" },
		func(s *computev1alpha.InstanceConsoleSession) { s.Spec.InstanceRef.UID = "uid-2" },
		func(s *computev1alpha.InstanceConsoleSession) { s.Spec.ContainerName = "sidecar" },
		func(s *computev1alpha.InstanceConsoleSession) { s.Spec.Command[1] = "/etc/shadow" },
		func(s *computev1alpha.InstanceConsoleSession) { s.Spec.TTL = &metav1.Duration{Duration: time.Hour} },
		func(s *computev1alpha.InstanceConsoleSession) { s.Spec.ClientPublicKey = strings.Repeat("b", 64) },
	}
	for i, mutate := range mutations {
		changed := session.DeepCopy()
		mutate(changed)
		if Verify(key, changed, "user@example.com", proof, now) {
			t.Errorf("accepted spec mutation %d", i)
		}
	}
}
